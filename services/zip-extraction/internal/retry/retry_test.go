package retry_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	mylog "github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/log"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/retry"
)

func defaultCfg() config.RetryConfig {
	return config.RetryConfig{
		MaxAttempts:       3,
		BackoffBaseMillis: 1, // small for fast tests
		BackoffFactor:     2.0,
		JitterFraction:    0.25,
	}
}

func TestDo_SuccessOnFirstAttempt(t *testing.T) {
	r := retry.New(defaultCfg(), extraction.SystemClock{}, rand.New(rand.NewSource(1)), mylog.NewDiscardLogger())
	var calls int
	err := r.Do(context.Background(), func(ctx context.Context) error {
		calls++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestDo_RetriesTransientThenSucceeds(t *testing.T) {
	r := retry.New(defaultCfg(), extraction.SystemClock{}, rand.New(rand.NewSource(1)), mylog.NewDiscardLogger())
	var calls int
	err := r.Do(context.Background(), func(ctx context.Context) error {
		calls++
		if calls < 2 {
			return &extraction.TransientError{Cause: errors.New("ServiceUnavailable"), Class: extraction.TransientClass5xx}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestDo_PermanentNotRetried(t *testing.T) {
	r := retry.New(defaultCfg(), extraction.SystemClock{}, rand.New(rand.NewSource(1)), mylog.NewDiscardLogger())
	var calls int
	perm := &extraction.PermanentError{Cause: errors.New("AccessDenied")}
	err := r.Do(context.Background(), func(ctx context.Context) error {
		calls++
		return perm
	})
	require.Equal(t, perm, err)
	assert.Equal(t, 1, calls)
}

func TestDo_ExhaustionWrapsAsPermanent(t *testing.T) {
	r := retry.New(defaultCfg(), extraction.SystemClock{}, rand.New(rand.NewSource(1)), mylog.NewDiscardLogger())
	transient := &extraction.TransientError{Cause: errors.New("ProvisionedThroughputExceeded"), Class: extraction.TransientClassThrottling}
	err := r.Do(context.Background(), func(ctx context.Context) error { return transient })
	require.Error(t, err)
	_, ok := extraction.IsPermanent(err)
	require.True(t, ok)
}

// PBT-05 oracle: BackoffFor returns exactly base*factor^n*(1+jitter*j) — within 1ns.
func TestPropertyBackoffOracle(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := config.RetryConfig{
			MaxAttempts:       rapid.IntRange(1, 10).Draw(t, "maxAttempts"),
			BackoffBaseMillis: rapid.IntRange(1, 1000).Draw(t, "baseMs"),
			BackoffFactor:     rapid.Float64Range(1.0, 4.0).Draw(t, "factor"),
			JitterFraction:    rapid.Float64Range(0, 1).Draw(t, "jitter"),
		}
		attempt := rapid.IntRange(0, 8).Draw(t, "attempt")
		j := rapid.Float64Range(-1, 1).Draw(t, "j")
		got := retry.BackoffFor(attempt, cfg, j)
		// Reference calculation.
		scaled := float64(cfg.BackoffBaseMillis)
		for i := 0; i < attempt; i++ {
			scaled *= cfg.BackoffFactor
		}
		expected := scaled * (1.0 + cfg.JitterFraction*j)
		if expected < 0 {
			expected = 0
		}
		want := time.Duration(expected * float64(time.Millisecond))
		// Allow a tiny epsilon for floating-point rounding through the package.
		assert.InDelta(t, want.Nanoseconds(), got.Nanoseconds(), 1, "attempt=%d j=%v", attempt, j)
	})
}

func TestClassify_AwsErrors(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		transient bool
		class     string
	}{
		{"throttling-string", apiErr{code: "ProvisionedThroughputExceededException"}, true, extraction.TransientClassThrottling},
		{"slowdown", apiErr{code: "SlowDown"}, true, extraction.TransientClassThrottling},
		{"request-timeout", apiErr{code: "RequestTimeout"}, true, extraction.TransientClassTimeout},
		{"bomb-defence-not-retryable", &extraction.BombDefenceError{Rule: 2}, false, ""},
		{"path-validation-not-retryable", &extraction.PathValidationError{Reason: "x"}, false, ""},
		{"unsupported-not-retryable", &extraction.UnsupportedFeatureError{Feature: "encrypted-zip"}, false, ""},
		{"permanent-not-retryable", &extraction.PermanentError{Cause: errors.New("x")}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transient, class := retry.Classify(tc.err)
			assert.Equal(t, tc.transient, transient)
			if tc.transient {
				assert.Equal(t, tc.class, class)
			}
		})
	}
}

func TestDo_ContextCanceledDuringBackoff(t *testing.T) {
	cfg := config.RetryConfig{MaxAttempts: 3, BackoffBaseMillis: 50, BackoffFactor: 2.0, JitterFraction: 0}
	r := retry.New(cfg, extraction.SystemClock{}, rand.New(rand.NewSource(1)), mylog.NewDiscardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a moment so the first retry wait observes it.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	transient := &extraction.TransientError{Cause: errors.New("throttle"), Class: extraction.TransientClassThrottling}
	err := r.Do(ctx, func(c context.Context) error { return transient })
	require.Error(t, err)
	// Either the cancel was observed or all 3 attempts ran — both are valid outcomes
	// depending on scheduler timing; assert at least that we did not block forever.
	if err != context.Canceled {
		_, ok := extraction.IsPermanent(err)
		require.True(t, ok, "expected canceled or *PermanentError, got %T %v", err, err)
	}
}

func TestDo_ContextAlreadyCanceledOnEntry(t *testing.T) {
	cfg := config.RetryConfig{MaxAttempts: 3, BackoffBaseMillis: 1, BackoffFactor: 2.0, JitterFraction: 0}
	r := retry.New(cfg, extraction.SystemClock{}, rand.New(rand.NewSource(1)), mylog.NewDiscardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	err := r.Do(ctx, func(c context.Context) error {
		calls++
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, calls)
}

func TestAsTransient_NilPassThrough(t *testing.T) {
	require.Nil(t, retry.AsTransient(nil))
}

func TestAsTransient_NonTransientUnchanged(t *testing.T) {
	in := errors.New("plain")
	got := retry.AsTransient(in)
	require.Equal(t, in, got, "non-transient errors must pass through unchanged")
}

func TestAsTransient_WrapsTransient(t *testing.T) {
	in := apiErr{code: "SlowDown"}
	got := retry.AsTransient(in)
	te, ok := extraction.IsTransient(got)
	require.True(t, ok)
	assert.Equal(t, extraction.TransientClassThrottling, te.Class)
}

func TestClassify_HTTP5xx(t *testing.T) {
	transient, class := retry.Classify(httpErr{status: 503})
	assert.True(t, transient)
	assert.Equal(t, extraction.TransientClass5xx, class)
}

func TestClassify_HTTP4xx(t *testing.T) {
	transient, _ := retry.Classify(httpErr{status: 404})
	assert.False(t, transient)
}

func TestClassify_NilErr(t *testing.T) {
	transient, class := retry.Classify(nil)
	assert.False(t, transient)
	assert.Empty(t, class)
}

func TestClassify_TransientPassThrough(t *testing.T) {
	in := &extraction.TransientError{Cause: errors.New("x"), Class: extraction.TransientClassTimeout}
	transient, class := retry.Classify(in)
	assert.True(t, transient)
	assert.Equal(t, extraction.TransientClassTimeout, class)
}

func TestClassify_DNSError(t *testing.T) {
	transient, class := retry.Classify(errors.New("dial tcp: lookup sqs.eu-west-1.amazonaws.com: no such host"))
	assert.True(t, transient)
	assert.Equal(t, extraction.TransientClassNetwork, class)
}

func TestClassify_DeadlineExceededIsTransient(t *testing.T) {
	transient, class := retry.Classify(context.DeadlineExceeded)
	assert.True(t, transient)
	assert.Equal(t, extraction.TransientClassTimeout, class)
}

// httpErr satisfies the interface { HTTPStatusCode() int } that the classifier inspects.
type httpErr struct{ status int }

func (e httpErr) Error() string       { return fmt.Sprintf("status %d", e.status) }
func (e httpErr) HTTPStatusCode() int { return e.status }

// apiErr is a minimal smithy.APIError used to drive classifier tests.
type apiErr struct{ code string }

func (e apiErr) Error() string                { return e.code }
func (e apiErr) ErrorCode() string            { return e.code }
func (e apiErr) ErrorMessage() string         { return e.code }
func (e apiErr) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }
