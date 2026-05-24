// Package retry implements classifier-driven exponential-backoff retry per
// FR-12 + BR-RETRY-001..014. Only *extraction.TransientError is retried;
// bomb-defence, path-validation, unsupported-feature, and *PermanentError
// are propagated immediately.
//
// BackoffFor is exported as the PBT-05 oracle: implementation delays MUST
// match the closed-form formula within ±1µs.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aws/smithy-go"
	"go.uber.org/zap"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

// Retrier wraps an operation invocation with retry on *TransientError per BR-RETRY-002.
type Retrier struct {
	cfg    config.RetryConfig
	clock  extraction.Clock
	rng    *rand.Rand
	rngMu  sync.Mutex
	logger extraction.Logger
}

// New constructs a Retrier with the given config and dependencies. rng is the
// random source used for jitter; tests inject a controlled source for
// determinism (PBT-08).
func New(cfg config.RetryConfig, clock extraction.Clock, rng *rand.Rand, logger extraction.Logger) *Retrier {
	return &Retrier{cfg: cfg, clock: clock, rng: rng, logger: logger}
}

// Do invokes op up to cfg.MaxAttempts times. Returns nil on success, the
// classified *TransientError wrapped in *PermanentError on retry exhaustion,
// or the originating error unchanged for non-retryable cases.
func (r *Retrier) Do(ctx context.Context, op func(ctx context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < r.cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op(ctx)
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		lastErr = err
		// Don't sleep after the last attempt.
		if attempt == r.cfg.MaxAttempts-1 {
			break
		}
		wait := r.computeBackoff(attempt)
		if r.logger != nil {
			r.logger.Warn("retry",
				zap.Int("attempt", attempt+1),
				zap.Int("maxAttempts", r.cfg.MaxAttempts),
				zap.Int64("waitMs", wait.Milliseconds()),
				zap.String("err", err.Error()),
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return &extraction.PermanentError{Cause: lastErr}
}

func (r *Retrier) computeBackoff(attempt int) time.Duration {
	r.rngMu.Lock()
	// rand.Float64() returns [0.0, 1.0); shift into [-1.0, 1.0).
	j := r.rng.Float64()*2 - 1
	r.rngMu.Unlock()
	return BackoffFor(attempt, r.cfg, j)
}

// BackoffFor computes the wait duration for attempt n (0-indexed) under the
// given config and a normalised jitter value j ∈ [-1.0, 1.0).
// Formula (BR-RETRY-003):
//
//	delay = baseMs * factor^n * (1 + jitterFraction * j)
//
// The returned duration is clamped to >= 0 to handle edge cases where extreme
// negative jitter could produce a negative delay.
func BackoffFor(attempt int, cfg config.RetryConfig, jitter float64) time.Duration {
	baseMs := float64(cfg.BackoffBaseMillis)
	scaled := baseMs * math.Pow(cfg.BackoffFactor, float64(attempt))
	adj := scaled * (1.0 + cfg.JitterFraction*jitter)
	if adj < 0 {
		adj = 0
	}
	return time.Duration(adj * float64(time.Millisecond))
}

// Classify inspects err and returns whether it is transient + its class
// (BR-RETRY-014). Caller wraps as *extraction.TransientError when retrying.
//
//nolint:gocyclo // exhaustive classification is intentionally flat
func Classify(err error) (transient bool, class string) {
	if err == nil {
		return false, ""
	}

	// Never retry the typed domain errors.
	if _, ok := extraction.IsBombDefence(err); ok {
		return false, ""
	}
	if _, ok := extraction.IsPathValidation(err); ok {
		return false, ""
	}
	if _, ok := extraction.IsUnsupportedFeature(err); ok {
		return false, ""
	}
	if _, ok := extraction.IsPermanent(err); ok {
		return false, ""
	}
	if te, ok := extraction.IsTransient(err); ok {
		return true, te.Class
	}

	// Throttling / 5xx via AWS SDK smithy-go APIError.
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		switch code {
		case "ProvisionedThroughputExceededException",
			"ProvisionedThroughputExceeded",
			"RequestLimitExceeded",
			"RequestThrottled",
			"Throttling",
			"ThrottlingException",
			"SlowDown":
			return true, extraction.TransientClassThrottling
		case "RequestTimeout",
			"RequestTimeoutException":
			return true, extraction.TransientClassTimeout
		}
	}

	// HTTP-status-coded errors. smithy-go exposes status via HTTPResponseError.
	var httpErr interface {
		HTTPStatusCode() int
	}
	if errors.As(err, &httpErr) {
		s := httpErr.HTTPStatusCode()
		switch {
		case s >= 500 && s <= 599:
			return true, extraction.TransientClass5xx
		case s >= 400 && s <= 499:
			return false, ""
		}
	}

	// Per-request DeadlineExceeded — NOT from the extraction context (the
	// caller is responsible for distinguishing; we conservatively classify
	// any DeadlineExceeded reaching us as timeout-class transient).
	if errors.Is(err, context.DeadlineExceeded) {
		return true, extraction.TransientClassTimeout
	}

	// Network errors.
	var netOp *net.OpError
	if errors.As(err, &netOp) {
		return true, extraction.TransientClassNetwork
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Temporary() {
		return true, extraction.TransientClassNetwork
	}

	// DNS errors (string-match fallback; smithy-go wraps these unhelpfully).
	if strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "dial tcp") {
		return true, extraction.TransientClassNetwork
	}

	return false, ""
}

// isRetryable wraps Classify to provide a boolean for Do's loop.
func isRetryable(err error) bool {
	transient, _ := Classify(err)
	return transient
}

// AsTransient classifies err and wraps it in *extraction.TransientError if
// applicable. Returns the original err if not transient. Used by adapters
// (storage / dynamodb / sqs) before returning to the orchestrator.
func AsTransient(err error) error {
	if err == nil {
		return nil
	}
	transient, class := Classify(err)
	if !transient {
		return err
	}
	return &extraction.TransientError{Cause: err, Class: class}
}

// Compile-time references to keep linters happy if the format helper falls out
// of use across refactors.
var _ = fmt.Sprintf
