// Package app is the top-level orchestrator that composes the SQS consumer,
// the HTTP operational server, and the graceful-drain coordination.
package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/health"
)

// Config holds App-level tunables.
type Config struct {
	GracefulShutdownTimeoutSec int
}

// Queue is the port for the SQS receive-loop.
type Queue interface {
	Run(ctx context.Context, handler func(ctx context.Context, msg extraction.ClaimCheck) (deleteMsg bool, err error)) error
}

// HTTPServer is the port for the operational HTTP server.
type HTTPServer interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// StartupProbe is invoked once during startup to verify reachability of
// dependencies (SQS / S3 / DynamoDB). Returns nil when ready.
type StartupProbe func(ctx context.Context) error

// Dependencies is the DI root.
type Dependencies struct {
	Logger     extraction.Logger
	Extractor  *extraction.Service
	Queue      Queue
	HTTPServer HTTPServer
	HealthGate *health.Gate
	Startup    StartupProbe // optional; nil → skip startup probe
}

// Service is the top-level application.
type Service struct {
	cfg  Config
	deps Dependencies
}

// New constructs a Service.
func New(cfg Config, deps Dependencies) *Service { return &Service{cfg: cfg, deps: deps} }

// Run starts all subsystems and blocks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	logger := s.deps.Logger

	// 1. Start HTTP server (liveness OK immediately; readiness false until startup probe passes).
	httpErrCh := make(chan error, 1)
	go func() {
		httpErrCh <- s.deps.HTTPServer.Start(ctx)
	}()

	// 2. Startup health checks.
	if s.deps.Startup != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.deps.Startup(probeCtx)
		cancel()
		if err != nil {
			logger.Error("startup probe failed", zap.Error(err))
			s.deps.HealthGate.SetReady(false)
			return fmt.Errorf("startup: %w", err)
		}
	}
	s.deps.HealthGate.SetReady(true)
	logger.Info("ready",
		zap.Int("gracefulShutdownTimeoutSec", s.cfg.GracefulShutdownTimeoutSec),
	)

	// 3. Start SQS receive-loop.
	queueDone := make(chan struct{})
	var queueErr error
	go func() {
		defer close(queueDone)
		queueErr = s.deps.Queue.Run(ctx, func(c context.Context, msg extraction.ClaimCheck) (bool, error) {
			outcome, err := s.deps.Extractor.Process(c, msg)
			if err != nil {
				return false, err // LEAVE message for SQS redrive
			}
			// Per BR-DLQ-001: DELETE on every terminal status.
			logger.Info("message processed",
				zap.String("pipelineExecutionId", msg.PipelineExecutionID),
				zap.String("status", outcome.Status.String()),
				zap.String("reason", outcome.Reason),
				zap.Int("entryCount", outcome.EntryCount),
				zap.Int("failureCount", outcome.FailureCount),
				zap.Int64("durationMs", outcome.DurationMs),
			)
			return true, nil
		})
	}()

	// 4. Block until ctx is cancelled OR a subsystem errors out.
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received; draining")
	case err := <-httpErrCh:
		if err != nil {
			logger.Error("http server failed", zap.Error(err))
			return err
		}
	case <-queueDone:
		if queueErr != nil {
			logger.Error("sqs run failed", zap.Error(queueErr))
			return queueErr
		}
	}

	// 5. Drain coordination.
	s.deps.HealthGate.SetReady(false)

	// Wait for queue to finish draining its workers (it handles its own deadline).
	drainDone := make(chan struct{})
	var once sync.Once
	go func() {
		<-queueDone
		once.Do(func() { close(drainDone) })
	}()
	select {
	case <-drainDone:
	case <-time.After(time.Duration(s.cfg.GracefulShutdownTimeoutSec+10) * time.Second):
		logger.Warn("app drain deadline hit; exiting anyway")
	}

	// Shut down HTTP server.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.deps.HTTPServer.Shutdown(shutdownCtx)
	logger.Info("clean shutdown complete")
	return nil
}
