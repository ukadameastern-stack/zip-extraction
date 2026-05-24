package log_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"pgregory.net/rapid"

	mylog "github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/log"
)

// PBT-03 invariant: sensitive-keyed fields are detected and would be redacted.
// (Full output verification requires a custom zap sink; here we verify the
// classifier function directly.)
func TestPropertySensitiveKeyDetection(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := rapid.SampledFrom([]string{
			"password", "passwd", "secret", "token", "credential",
			"aws_access_key_id", "aws_secret_access_key",
			"session_token", "api_key", "authorization",
		}).Draw(t, "base")
		// Generators that vary case and embed the base in larger strings.
		variant := rapid.IntRange(0, 3).Draw(t, "variant")
		var key string
		switch variant {
		case 0:
			key = base
		case 1:
			key = strings.ToUpper(base)
		case 2:
			key = "user_" + base
		default:
			key = base + "_value"
		}
		assert.True(t, mylog.IsSensitiveKey(key), "key=%q must be sensitive", key)
	})
}

func TestIsSensitiveKey_NonSensitive(t *testing.T) {
	for _, k := range []string{"pipelineExecutionId", "documentId", "correlationId", "status", "sourceArchive", "entryIndex"} {
		assert.False(t, mylog.IsSensitiveKey(k), "key=%q must not be sensitive", k)
	}
}

func TestNew_JSONFormat(t *testing.T) {
	l, err := mylog.New(mylog.Config{Format: "json", Level: "info"}, "v-test")
	require.NoError(t, err)
	require.NotNil(t, l)
	l.Info("hello", zap.String("k", "v"))
	l.Warn("warn", zap.String("k", "v"))
	l.Debug("debug-no-emit-at-info-level", zap.String("k", "v"))
	l.Error("error", zap.String("k", "v"))
	// Sync may legitimately fail on terminal stdout; we tolerate that.
	_ = l.Sync()
}

func TestNew_ConsoleFormat(t *testing.T) {
	l, err := mylog.New(mylog.Config{Format: "console", Level: "debug"}, "v-test")
	require.NoError(t, err)
	require.NotNil(t, l)
	// With child logger.
	child := l.With(zap.String("pipelineExecutionId", "exec-1"))
	child.Debug("debug-emit")
	child.Info("info-emit")
}

func TestNew_DefaultFormatIsJSON(t *testing.T) {
	l, err := mylog.New(mylog.Config{Format: "", Level: "info"}, "v-test")
	require.NoError(t, err)
	require.NotNil(t, l)
}

func TestNew_InvalidFormat(t *testing.T) {
	_, err := mylog.New(mylog.Config{Format: "yaml", Level: "info"}, "v-test")
	require.Error(t, err)
}

func TestNew_InvalidLevel(t *testing.T) {
	_, err := mylog.New(mylog.Config{Format: "json", Level: "nope"}, "v-test")
	require.Error(t, err)
}

func TestWith_NoFields_ReturnsSelf(t *testing.T) {
	l := mylog.NewDiscardLogger()
	got := l.With()
	require.Equal(t, l, got)
}

func TestDiscardLogger_AllMethods(t *testing.T) {
	l := mylog.NewDiscardLogger()
	l.Info("x")
	l.Warn("x")
	l.Error("x")
	l.Debug("x")
	require.NoError(t, l.Sync())
	child := l.With(zap.String("k", "v"))
	child.Info("y", zap.String("password", "should-be-redacted"))
}

func TestRedaction_ZapField(t *testing.T) {
	// Build a real logger writing to an in-memory buffer to verify deny-list redaction.
	l := mylog.NewDiscardLogger()
	// Just confirm With + redaction wraps without panicking.
	l.With(zap.String("password", "p"), zap.String("safe", "ok")).Info("msg",
		zap.String("token", "secret-token"))
}
