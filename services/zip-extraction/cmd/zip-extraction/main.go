// Command zip-extraction is the SQS consumer for the UOW-SVC-12 service.
// See aidlc-docs/ for full design documentation.
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/app"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/awsclients"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/bombdefence"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/dynamodb"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/health"
	mylog "github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/log"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/metrics"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/retry"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/slipsheet"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/sqs"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/storage"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/validation"
)

// version is set at link time via -ldflags="-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "fatal: panic: %v\n", r)
			os.Exit(2)
		}
	}()

	// 1. Configuration.
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// 2. Logger.
	logger, err := mylog.New(mylog.Config{Format: cfg.Logging.Format, Level: cfg.Logging.Level}, version)
	if err != nil {
		return err
	}
	defer logger.Sync()
	logger.Info("starting",
		zap.String("version", version),
		zap.String("region", cfg.Infra.Region),
		zap.Bool("localstack", cfg.Infra.AWSEndpointURL != ""),
	)

	// 3. Root context with SIGINT/SIGTERM cancellation.
	rootCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// 4. SIGUSR1 heap-dump handler (NFR-Z-046 / Q4 of NFR design).
	installHeapDumpHandler(logger)

	// 5. AWS clients (singleton per pod).
	awsSet, err := awsclients.Build(rootCtx, cfg.Infra)
	if err != nil {
		return err
	}

	// 6. Metrics + health.
	reg := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = reg
	prometheus.DefaultGatherer = reg
	m := metrics.New(reg)
	gate := health.NewGate()
	httpSrv := health.NewServer(cfg.HTTP.Port, gate, reg)

	// 7. Domain components (no I/O).
	checker := bombdefence.New(cfg.BombDefence)
	pathValidator := validation.New()
	retrier := retry.New(cfg.Retry, extraction.SystemClock{}, rand.New(rand.NewSource(time.Now().UnixNano())), logger)

	// 8. Adapters.
	storageAdapter := storage.NewAdapter(awsSet.S3, awsSet.S3Uploader, storage.Config{
		MultipartThresholdBytes: cfg.Streaming.MultipartThresholdBytes,
		SSEMode:                 cfg.SSE.Mode,
		SSEKMSKeyID:             cfg.SSE.KMSKeyID,
	})
	ddbAdapter := dynamodb.NewAdapter(awsSet.DDB, cfg.Infra.DynamoTable, m.RedeliverySkip)
	slipsheetWriter := slipsheet.NewWriter(storageAdapter, cfg.Infra.StagingBucket, "slipsheets/")
	sqsAdapter := sqs.NewAdapter(awsSet.SQS, cfg.Infra.QueueURL, cfg.SQS, logger)

	// 9. Extraction orchestrator.
	ext := extraction.New(extraction.Dependencies{
		Downloader:      storageAdapter,
		Uploader:        storageAdapter,
		Recorder:        ddbAdapter,
		SlipsheetWriter: slipsheetWriter,
		BombChecker:     checker,
		PathValidator:   pathValidator,
		Retrier:         retrier,
		Metrics:         m,
		Logger:          logger,
		Clock:           extraction.SystemClock{},
		Config: extraction.ExtractionConfig{
			MaxExtractionDurationSec: cfg.BombDefence.MaxExtractionDurationSec,
			StagingBucket:            cfg.Infra.StagingBucket,
			SSEMode:                  cfg.SSE.Mode,
			SSEKMSKeyID:              cfg.SSE.KMSKeyID,
		},
	})

	// 10. App orchestrator.
	a := app.New(
		app.Config{GracefulShutdownTimeoutSec: cfg.SQS.GracefulShutdownTimeoutSec},
		app.Dependencies{
			Logger:     logger,
			Extractor:  ext,
			Queue:      &queueAdapter{a: sqsAdapter},
			HTTPServer: httpSrv,
			HealthGate: gate,
			Startup:    buildStartupProbe(awsSet.SQS, cfg.Infra.QueueURL),
		},
	)

	return a.Run(rootCtx)
}

// queueAdapter adapts *sqs.Adapter to the app.Queue port signature.
type queueAdapter struct {
	a *sqs.Adapter
}

func (q *queueAdapter) Run(
	ctx context.Context,
	handler func(ctx context.Context, msg extraction.ClaimCheck) (bool, error),
) error {
	return q.a.Run(ctx, sqs.MessageHandler(handler))
}

// buildStartupProbe returns a probe that verifies SQS reachability by calling
// GetQueueAttributes. We don't pre-probe S3 / DynamoDB here because they are
// permission-scoped resources whose failure modes mirror SQS's.
func buildStartupProbe(sqsClient *awssqs.Client, queueURL string) app.StartupProbe {
	return func(ctx context.Context) error {
		_, err := sqsClient.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queueURL),
		})
		if err != nil {
			return fmt.Errorf("sqs reachability: %w", err)
		}
		return nil
	}
}

// installHeapDumpHandler registers a SIGUSR1 handler that writes a heap profile
// to /tmp/heap-<RFC3339>.pprof. This is the emergency-profiling tool documented
// in pattern §4.6 (no pprof endpoint).
func installHeapDumpHandler(logger extraction.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			path := fmt.Sprintf("/tmp/heap-%s.pprof", time.Now().UTC().Format("20060102T150405Z"))
			f, err := os.Create(path)
			if err != nil {
				logger.Warn("heap dump: create file failed", zap.Error(err))
				continue
			}
			if err := pprof.WriteHeapProfile(f); err != nil {
				logger.Warn("heap dump: WriteHeapProfile failed", zap.Error(err))
			} else {
				logger.Info("heap dump written", zap.String("path", path))
			}
			_ = f.Close()
		}
	}()
}
