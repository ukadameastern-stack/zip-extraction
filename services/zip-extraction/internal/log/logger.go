// Package log provides a structured logger built on go.uber.org/zap with
// runtime-selectable JSON / console formats (Q10 of requirements) and a
// sensitive-field deny-list filter that masks fields whose keys match secret
// patterns (BR-LOG-002, SECURITY-03).
//
// The Logger interface is intentionally narrow: it's the consumer-defined port
// every other package depends on for logging. The concrete implementation
// (*zapLogger) wraps a *zap.Logger and is returned by New.
package log

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field is an alias for zap.Field so callers do not need to import zap directly.
type Field = zap.Field

// Logger is the structured-logger interface consumed by every other package
// in the zip-extraction service. Implementations MUST honour the sensitive-field
// deny-list (BR-LOG-002).
type Logger interface {
	With(fields ...Field) Logger
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	Debug(msg string, fields ...Field)
	Sync() error
}

// Config holds the logger configuration loaded from env vars at startup.
type Config struct {
	Format string // "json" | "console"
	Level  string // "info" | "debug" | "warn" | "error"
}

// New constructs a Logger honouring Q10 of requirements:
//   - Format "json"    → zap production config (production default)
//   - Format "console" → zap development config with colour (local dev)
//
// The constant fields service and version are bound to every entry produced
// by this logger.
func New(cfg Config, version string) (Logger, error) {
	level, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("log: parse level %q: %w", cfg.Level, err)
	}

	var zapCfg zap.Config
	switch strings.ToLower(cfg.Format) {
	case "console":
		zapCfg = zap.NewDevelopmentConfig()
		zapCfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	case "", "json":
		zapCfg = zap.NewProductionConfig()
	default:
		return nil, fmt.Errorf("log: unsupported format %q (want json|console)", cfg.Format)
	}
	zapCfg.Level = zap.NewAtomicLevelAt(level)
	zapCfg.OutputPaths = []string{"stdout"}
	zapCfg.ErrorOutputPaths = []string{"stderr"}
	zapCfg.InitialFields = map[string]interface{}{
		"service": "zip-extraction",
		"version": version,
	}

	zl, err := zapCfg.Build(zap.AddCallerSkip(1))
	if err != nil {
		return nil, fmt.Errorf("log: build zap: %w", err)
	}
	return &zapLogger{z: zl}, nil
}

// NewDiscardLogger returns a Logger that discards all entries. Intended for
// unit tests that don't care about log output.
func NewDiscardLogger() Logger {
	return &zapLogger{z: zap.NewNop()}
}

type zapLogger struct {
	z *zap.Logger
}

func (l *zapLogger) With(fields ...Field) Logger {
	if len(fields) == 0 {
		return l
	}
	return &zapLogger{z: l.z.With(filterSensitive(fields)...)}
}

func (l *zapLogger) Info(msg string, fields ...Field) {
	l.z.Info(msg, filterSensitive(fields)...)
}

func (l *zapLogger) Warn(msg string, fields ...Field) {
	l.z.Warn(msg, filterSensitive(fields)...)
}

func (l *zapLogger) Error(msg string, fields ...Field) {
	l.z.Error(msg, filterSensitive(fields)...)
}

func (l *zapLogger) Debug(msg string, fields ...Field) {
	l.z.Debug(msg, filterSensitive(fields)...)
}

func (l *zapLogger) Sync() error {
	// Sync's behaviour on stdout is platform-dependent; ignore "invalid argument"
	// from terminal stdout but propagate other errors.
	if err := l.z.Sync(); err != nil {
		if isSyncStdoutBenign(err) {
			return nil
		}
		fmt.Fprintf(os.Stderr, "log: sync: %v\n", err)
		return err
	}
	return nil
}

// sensitiveKeyPatterns lists case-insensitive substring matches that cause a
// field's value to be replaced with [REDACTED] before emission. The list
// MUST stay synchronised with BR-LOG-002.
var sensitiveKeyPatterns = []string{
	"password", "passwd", "secret", "token", "credential",
	"aws_access_key_id", "aws_secret_access_key", "session_token",
	"api_key", "authorization",
}

// filterSensitive returns a copy of fields with sensitive values masked.
// The original slice is not modified.
func filterSensitive(fields []Field) []Field {
	if len(fields) == 0 {
		return fields
	}
	out := make([]Field, len(fields))
	for i, f := range fields {
		if isSensitiveKey(f.Key) {
			out[i] = zap.String(f.Key, "[REDACTED]")
		} else {
			out[i] = f
		}
	}
	return out
}

// IsSensitiveKey reports whether a field key matches the deny-list. Exported
// for PBT tests in internal/log_test.
func IsSensitiveKey(key string) bool {
	return isSensitiveKey(key)
}

func isSensitiveKey(key string) bool {
	lk := strings.ToLower(key)
	for _, p := range sensitiveKeyPatterns {
		if strings.Contains(lk, p) {
			return true
		}
	}
	return false
}

// isSyncStdoutBenign returns true for errors that are expected when calling
// Sync on a terminal-backed stdout (Linux returns EINVAL).
func isSyncStdoutBenign(err error) bool {
	s := err.Error()
	return strings.Contains(s, "invalid argument") || strings.Contains(s, "inappropriate ioctl")
}
