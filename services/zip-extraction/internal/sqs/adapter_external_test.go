package sqs_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	mylog "github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/log"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/sqs"
)

// fakeSQSAPI implements sqs.SQSAPI with controllable behaviour.
type fakeSQSAPI struct {
	mu sync.Mutex

	receiveBatches  [][]sqstypes.Message
	deleteCalls     atomic.Int32
	heartbeatCalls  atomic.Int32
	receiveCalls    atomic.Int32
	emptyAfterDrain bool

	deleteErr    error
	heartbeatErr error
	receiveErr   error
}

func (f *fakeSQSAPI) ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	f.receiveCalls.Add(1)
	if f.receiveErr != nil {
		return nil, f.receiveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.receiveBatches) == 0 {
		// Long-poll: wait briefly so the test isn't a tight spin.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		return &awssqs.ReceiveMessageOutput{}, nil
	}
	batch := f.receiveBatches[0]
	f.receiveBatches = f.receiveBatches[1:]
	return &awssqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (f *fakeSQSAPI) DeleteMessage(_ context.Context, _ *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.deleteCalls.Add(1)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &awssqs.DeleteMessageOutput{}, nil
}

func (f *fakeSQSAPI) ChangeMessageVisibility(_ context.Context, _ *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	f.heartbeatCalls.Add(1)
	if f.heartbeatErr != nil {
		return nil, f.heartbeatErr
	}
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

func validBody(execID string) string {
	return `{
		"pipelineExecutionId":"` + execID + `",
		"tenantId":"tenant-1",
		"documentId":"doc-1",
		"sourceBucket":"bucket",
		"sourceKey":"uploads/x.zip",
		"correlationId":"corr-1"
	}`
}

func mkMsg(execID string) sqstypes.Message {
	body := validBody(execID)
	rh := "receipt-" + execID
	return sqstypes.Message{Body: &body, ReceiptHandle: &rh, MessageId: aws.String("id-" + execID)}
}

func defaultSQSCfg() config.SQSConfig {
	return config.SQSConfig{
		HeartbeatIntervalSec:       1,
		MaxInFlight:                2,
		GracefulShutdownTimeoutSec: 2,
		VisibilityTimeoutSec:       300,
	}
}

func TestAdapter_Run_DeliversMessagesAndDeletes(t *testing.T) {
	api := &fakeSQSAPI{
		receiveBatches: [][]sqstypes.Message{{mkMsg("e1"), mkMsg("e2")}},
	}
	a := sqs.NewAdapter(api, "https://sqs/test", defaultSQSCfg(), mylog.NewDiscardLogger())

	var got []string
	var gotMu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = a.Run(ctx, func(c context.Context, m extraction.ClaimCheck) (bool, error) {
			gotMu.Lock()
			got = append(got, m.PipelineExecutionID)
			gotMu.Unlock()
			return true, nil
		})
		close(done)
	}()

	// Wait until both messages are processed and deleted.
	require.Eventually(t, func() bool {
		gotMu.Lock()
		defer gotMu.Unlock()
		return len(got) == 2
	}, 3*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		return api.deleteCalls.Load() >= 2
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	<-done
}

func TestAdapter_Run_HandlerReturnsFalseDoesNotDelete(t *testing.T) {
	api := &fakeSQSAPI{receiveBatches: [][]sqstypes.Message{{mkMsg("e1")}}}
	a := sqs.NewAdapter(api, "https://sqs/test", defaultSQSCfg(), mylog.NewDiscardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handled := make(chan struct{}, 1)
	go func() {
		_ = a.Run(ctx, func(c context.Context, m extraction.ClaimCheck) (bool, error) {
			handled <- struct{}{}
			return false, errors.New("leave for redrive")
		})
	}()

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not invoked in time")
	}
	cancel()
	// DeleteMessage should NOT have been called.
	time.Sleep(50 * time.Millisecond) // small grace for shutdown
	assert.Equal(t, int32(0), api.deleteCalls.Load())
}

func TestAdapter_Run_SchemaInvalidBodyIsDeleted(t *testing.T) {
	bad := `{not-json`
	rh := "rh-x"
	msg := sqstypes.Message{Body: &bad, ReceiptHandle: &rh, MessageId: aws.String("id-x")}
	api := &fakeSQSAPI{receiveBatches: [][]sqstypes.Message{{msg}}}
	a := sqs.NewAdapter(api, "https://sqs/test", defaultSQSCfg(), mylog.NewDiscardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var handlerCalls atomic.Int32
	go func() {
		_ = a.Run(ctx, func(c context.Context, m extraction.ClaimCheck) (bool, error) {
			handlerCalls.Add(1)
			return true, nil
		})
	}()

	require.Eventually(t, func() bool {
		return api.deleteCalls.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond)
	assert.Equal(t, int32(0), handlerCalls.Load(), "handler MUST NOT be called for schema-invalid messages")
	cancel()
}

func TestAdapter_Run_ReceiveErrorIsLoggedAndLoopContinues(t *testing.T) {
	api := &fakeSQSAPI{receiveErr: errors.New("transient receive fail")}
	a := sqs.NewAdapter(api, "https://sqs/test", defaultSQSCfg(), mylog.NewDiscardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = a.Run(ctx, func(c context.Context, m extraction.ClaimCheck) (bool, error) { return true, nil })
	}()
	// Let the receive error fire at least once.
	require.Eventually(t, func() bool { return api.receiveCalls.Load() >= 1 }, 2*time.Second, 20*time.Millisecond)
	cancel()
}

func TestAdapter_Run_HeartbeatFires(t *testing.T) {
	// Block handler so the heartbeat ticker can fire.
	api := &fakeSQSAPI{receiveBatches: [][]sqstypes.Message{{mkMsg("e1")}}}
	cfg := defaultSQSCfg()
	cfg.HeartbeatIntervalSec = 1
	a := sqs.NewAdapter(api, "https://sqs/test", cfg, mylog.NewDiscardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	holdHandler := make(chan struct{})
	go func() {
		_ = a.Run(ctx, func(c context.Context, m extraction.ClaimCheck) (bool, error) {
			<-holdHandler
			return true, nil
		})
	}()

	// Wait for at least one heartbeat ChangeMessageVisibility call.
	require.Eventually(t, func() bool {
		return api.heartbeatCalls.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected ≥1 heartbeat call")

	close(holdHandler)
	cancel()
}

func TestAdapter_Run_BoundedWorkerPool(t *testing.T) {
	// Dispatch 5 messages with maxInFlight=2 — at most 2 should be in-flight concurrently.
	msgs := []sqstypes.Message{mkMsg("a"), mkMsg("b"), mkMsg("c"), mkMsg("d"), mkMsg("e")}
	api := &fakeSQSAPI{receiveBatches: [][]sqstypes.Message{msgs}}
	cfg := defaultSQSCfg()
	cfg.MaxInFlight = 2
	a := sqs.NewAdapter(api, "https://sqs/test", cfg, mylog.NewDiscardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var concurrent atomic.Int32
	var peak atomic.Int32
	var completed atomic.Int32

	go func() {
		_ = a.Run(ctx, func(c context.Context, m extraction.ClaimCheck) (bool, error) {
			n := concurrent.Add(1)
			defer concurrent.Add(-1)
			if n > peak.Load() {
				peak.Store(n)
			}
			time.Sleep(80 * time.Millisecond)
			completed.Add(1)
			return true, nil
		})
	}()

	require.Eventually(t, func() bool { return completed.Load() == 5 }, 5*time.Second, 20*time.Millisecond)
	cancel()
	assert.LessOrEqual(t, peak.Load(), int32(2), "peak concurrency exceeded maxInFlight=2")
}
