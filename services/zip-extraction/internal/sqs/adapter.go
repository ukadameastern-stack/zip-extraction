// Package sqs is the SQS receive-loop + worker-pool adapter implementing
// the dispatch contract for the App orchestrator. It enforces Q2 of
// application design (single long-poll receiver + bounded worker pool) and
// FR-9 + Q6 of NFR design (per-message visibility heartbeat resetting to
// 300 s). Graceful drain follows Q7 of application design / BR-DRAIN-*.
package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/zap"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

// SQSAPI is the minimum SDK surface this adapter uses.
type SQSAPI interface {
	ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *awssqs.DeleteMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *awssqs.ChangeMessageVisibilityInput, optFns ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
}

// MessageHandler is invoked once per received SQS message.
//
// Return values dictate disposition:
//   - (deleteMsg=true, err=nil)         → DeleteMessage; record SUCCESS / PARTIAL_FAILED / FAILED outcome
//   - (deleteMsg=true, err=non-nil)     → DeleteMessage anyway (deterministic failure); err is logged
//   - (deleteMsg=false, err=non-nil)    → LEAVE message; SQS native redrive after visibility expiry
type MessageHandler func(ctx context.Context, msg extraction.ClaimCheck) (deleteMsg bool, err error)

// Adapter is the SQS consumer service.
type Adapter struct {
	api      SQSAPI
	queueURL string
	cfg      config.SQSConfig
	logger   extraction.Logger
}

// NewAdapter constructs an Adapter.
func NewAdapter(api SQSAPI, queueURL string, cfg config.SQSConfig, logger extraction.Logger) *Adapter {
	return &Adapter{api: api, queueURL: queueURL, cfg: cfg, logger: logger}
}

// Run starts the receive-loop. Blocks until ctx is cancelled. After cancellation,
// drains in-flight workers for up to cfg.GracefulShutdownTimeoutSec, then returns.
func (a *Adapter) Run(ctx context.Context, handler MessageHandler) error {
	slots := make(chan struct{}, a.cfg.MaxInFlight)
	var wg sync.WaitGroup

	receiveCtx, stopReceiving := context.WithCancel(ctx)

	go func() {
		<-ctx.Done()
		stopReceiving()
	}()

	for {
		if err := receiveCtx.Err(); err != nil {
			break
		}
		out, err := a.api.ReceiveMessage(receiveCtx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(a.queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     20,
			VisibilityTimeout:   int32(a.cfg.VisibilityTimeoutSec),
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
			a.logger.Warn("sqs receive failed", zap.Error(err))
			select {
			case <-receiveCtx.Done():
				goto drain
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, m := range out.Messages {
			m := m // capture
			select {
			case slots <- struct{}{}:
			case <-receiveCtx.Done():
				goto drain
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				a.dispatch(ctx, m, handler)
			}()
		}
	}

drain:
	a.logger.Info("sqs drain started",
		zap.Int("gracefulShutdownTimeoutSec", a.cfg.GracefulShutdownTimeoutSec),
	)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		a.logger.Info("sqs drain complete (all workers finished)")
	case <-time.After(time.Duration(a.cfg.GracefulShutdownTimeoutSec) * time.Second):
		a.logger.Warn("sqs drain deadline hit; remaining workers will be reclaimed via SQS visibility")
	}
	return nil
}

func (a *Adapter) dispatch(rootCtx context.Context, m sqstypes.Message, handler MessageHandler) {
	defer func() {
		if r := recover(); r != nil {
			a.logger.Error("worker panic",
				zap.Any("recover", r),
				zap.Stringp("messageId", m.MessageId),
			)
			// LEAVE message: don't delete on panic (BR-DLQ-002).
		}
	}()

	// Per-worker context. Inherits from rootCtx so SIGTERM cascades but its
	// own cancellation does not cascade upward (we only cancel it to stop the
	// heartbeat goroutine on completion).
	workerCtx, workerCancel := context.WithCancel(rootCtx)
	defer workerCancel()

	// Per-message heartbeat goroutine (FR-9 + BR-HEARTBEAT-001).
	heartbeatStopped := a.startHeartbeat(workerCtx, m.ReceiptHandle)

	msg, err := parseMessage(m.Body)
	if err != nil {
		a.logger.Warn("sqs: message schema invalid",
			zap.Stringp("messageId", m.MessageId),
			zap.Error(err),
		)
		// Delete on schema failure — re-delivery cannot fix this.
		a.deleteMessage(rootCtx, m.ReceiptHandle)
		workerCancel()
		<-heartbeatStopped
		return
	}

	deleteMsg, hErr := handler(workerCtx, msg)
	if hErr != nil {
		a.logger.Warn("sqs: handler returned error",
			zap.String("pipelineExecutionId", msg.PipelineExecutionID),
			zap.Error(hErr),
		)
	}
	workerCancel()
	<-heartbeatStopped

	if deleteMsg {
		a.deleteMessage(rootCtx, m.ReceiptHandle)
	}
}

func (a *Adapter) deleteMessage(ctx context.Context, receiptHandle *string) {
	if receiptHandle == nil {
		return
	}
	_, err := a.api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(a.queueURL),
		ReceiptHandle: receiptHandle,
	})
	if err != nil {
		a.logger.Warn("sqs: deleteMessage failed", zap.Error(err))
	}
}

// startHeartbeat spawns a goroutine that calls ChangeMessageVisibility(300s)
// every cfg.HeartbeatIntervalSec until ctx is cancelled. Returns a channel
// that is closed when the goroutine exits — caller waits on it before issuing
// DeleteMessage to avoid racing the heartbeat against the delete.
func (a *Adapter) startHeartbeat(ctx context.Context, receiptHandle *string) <-chan struct{} {
	stopped := make(chan struct{})
	if receiptHandle == nil {
		close(stopped)
		return stopped
	}
	visibility := int32(a.cfg.VisibilityTimeoutSec)
	if visibility <= 0 {
		visibility = 300
	}
	interval := time.Duration(a.cfg.HeartbeatIntervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := a.api.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
					QueueUrl:          aws.String(a.queueURL),
					ReceiptHandle:     receiptHandle,
					VisibilityTimeout: visibility,
				})
				if err == nil {
					continue
				}
				if isBenignHeartbeatErr(err) {
					a.logger.Warn("heartbeat: receipt invalid or message not in flight", zap.Error(err))
					return
				}
				a.logger.Warn("heartbeat: ChangeMessageVisibility failed", zap.Error(err))
			}
		}
	}()
	return stopped
}

// parseMessage decodes the JSON body into a ClaimCheck and validates the schema (FR-1.2).
func parseMessage(body *string) (extraction.ClaimCheck, error) {
	if body == nil || *body == "" {
		return extraction.ClaimCheck{}, fmt.Errorf("empty body")
	}
	var msg extraction.ClaimCheck
	if err := json.Unmarshal([]byte(*body), &msg); err != nil {
		return extraction.ClaimCheck{}, fmt.Errorf("json: %w", err)
	}
	if err := validateClaimCheck(msg); err != nil {
		return extraction.ClaimCheck{}, err
	}
	return msg, nil
}

func validateClaimCheck(m extraction.ClaimCheck) error {
	if m.PipelineExecutionID == "" {
		return fmt.Errorf("pipelineExecutionId: required")
	}
	if m.TenantID == "" {
		return fmt.Errorf("tenantId: required")
	}
	if m.DocumentID == "" {
		return fmt.Errorf("documentId: required")
	}
	if m.SourceBucket == "" {
		return fmt.Errorf("sourceBucket: required")
	}
	if m.SourceKey == "" {
		return fmt.Errorf("sourceKey: required")
	}
	if m.CorrelationID == "" {
		return fmt.Errorf("correlationId: required")
	}
	return nil
}

// isBenignHeartbeatErr reports whether err is one we should log+exit-quietly on.
func isBenignHeartbeatErr(err error) bool {
	if err == nil {
		return false
	}
	var apiErr interface {
		ErrorCode() string
	}
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ReceiptHandleIsInvalid", "MessageNotInflight":
			return true
		}
	}
	return false
}
