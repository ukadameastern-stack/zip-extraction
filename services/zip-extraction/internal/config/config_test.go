package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func validYAML() string {
	return `
bombDefence:
  maxCompressedSizeBytes:     524288000
  maxExtractedSizeBytes:      2147483648
  maxCompressionRatio:        100
  maxEntryCount:              10000
  maxDirectoryDepth:          10
  maxSingleFileSizeBytes:     262144000
  maxExtractionDurationSec:   240
  maxTotalDeclaredUncompressedBytes: 53687091200
streaming:
  maxInMemoryBufferBytes:     4194304
  multipartThresholdBytes:    5242880
retry:
  maxAttempts:                3
  backoffBaseMillis:          200
  backoffFactor:              2.0
  jitterFraction:             0.25
sqs:
  heartbeatIntervalSec:       30
  maxInFlight:                5
  gracefulShutdownTimeoutSec: 250
  visibilityTimeoutSec:       300
`
}

func setEnvAll(t *testing.T, yamlPath string) {
	t.Helper()
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("QUEUE_URL", "https://sqs.eu-west-1.amazonaws.com/000000000000/test")
	t.Setenv("STAGING_BUCKET", "test-bucket")
	t.Setenv("DYNAMO_TABLE", "pipeline_files")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("HTTP_PORT", "8080")
	t.Setenv("SSE_MODE", "SSE-S3")
	t.Setenv("CONFIG_PATH", yamlPath)
}

func TestLoad_HappyPath(t *testing.T) {
	setEnvAll(t, writeYAML(t, validYAML()))
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", cfg.Infra.Region)
	assert.Equal(t, 8080, cfg.HTTP.Port)
	assert.Equal(t, "SSE-S3", cfg.SSE.Mode)
	assert.Equal(t, 5, cfg.SQS.MaxInFlight)
}

func TestLoad_UnknownYAMLKeyRejected(t *testing.T) {
	bad := validYAML() + "\nunknownKey: true\n"
	setEnvAll(t, writeYAML(t, bad))
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml")
}

func TestLoad_MissingRequiredEnvVar(t *testing.T) {
	setEnvAll(t, writeYAML(t, validYAML()))
	t.Setenv("QUEUE_URL", "")
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queueURL")
}

func TestLoad_DefaultsApplied(t *testing.T) {
	// Clear environment so defaults are exercised.
	t.Setenv("AWS_REGION", "")
	t.Setenv("DYNAMO_TABLE", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("HTTP_PORT", "")
	t.Setenv("SSE_MODE", "")
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("SSE_KMS_KEY_ID", "")
	t.Setenv("QUEUE_URL", "https://x/y")
	t.Setenv("STAGING_BUCKET", "b")
	t.Setenv("CONFIG_PATH", writeYAML(t, validYAML()))

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", cfg.Infra.Region)
	assert.Equal(t, "pipeline_files", cfg.Infra.DynamoTable)
	assert.Equal(t, "json", cfg.Logging.Format)
	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, 8080, cfg.HTTP.Port)
	assert.Equal(t, "SSE-S3", cfg.SSE.Mode)
}

func TestLoad_InvalidHTTPPort(t *testing.T) {
	setEnvAll(t, writeYAML(t, validYAML()))
	t.Setenv("HTTP_PORT", "not-a-number")
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP_PORT")
}

func TestLoad_PortOutOfRange(t *testing.T) {
	setEnvAll(t, writeYAML(t, validYAML()))
	t.Setenv("HTTP_PORT", "99999")
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http.port")
}

func TestLoad_MissingYAMLFile(t *testing.T) {
	setEnvAll(t, writeYAML(t, validYAML()))
	t.Setenv("CONFIG_PATH", "/no/such/path.yaml")
	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml")
}

func TestValidate_VariousBadValues(t *testing.T) {
	base := func() config.Config {
		return config.Config{
			Infra:   config.InfraConfig{QueueURL: "x", StagingBucket: "y", DynamoTable: "z"},
			HTTP:    config.HTTPConfig{Port: 8080},
			Logging: config.LoggingConfig{Format: "json", Level: "info"},
			SSE:     config.SSEConfig{Mode: "SSE-S3"},
			BombDefence: config.BombDefenceConfig{
				MaxCompressedSizeBytes: 1, MaxExtractedSizeBytes: 1, MaxCompressionRatio: 1,
				MaxEntryCount: 1, MaxDirectoryDepth: 1,
				MaxSingleFileSizeBytes: 1, MaxExtractionDurationSec: 1,
				MaxTotalDeclaredUncompressedBytes: 1,
			},
			Streaming: config.StreamingConfig{MaxInMemoryBufferBytes: 1, MultipartThresholdBytes: 5 * 1024 * 1024},
			Retry:     config.RetryConfig{MaxAttempts: 1, BackoffBaseMillis: 1, BackoffFactor: 1, JitterFraction: 0},
			SQS:       config.SQSConfig{HeartbeatIntervalSec: 1, MaxInFlight: 1, GracefulShutdownTimeoutSec: 1, VisibilityTimeoutSec: 1},
		}
	}
	cases := []struct {
		name string
		mut  func(c *config.Config)
		want string
	}{
		{"empty-queue", func(c *config.Config) { c.Infra.QueueURL = "" }, "queueURL"},
		{"empty-bucket", func(c *config.Config) { c.Infra.StagingBucket = "" }, "stagingBucket"},
		{"empty-table", func(c *config.Config) { c.Infra.DynamoTable = "" }, "dynamoTable"},
		{"bad-port", func(c *config.Config) { c.HTTP.Port = 0 }, "http.port"},
		{"bad-port-high", func(c *config.Config) { c.HTTP.Port = 70000 }, "http.port"},
		{"bad-log-format", func(c *config.Config) { c.Logging.Format = "yaml" }, "logging.format"},
		{"bad-sse-mode", func(c *config.Config) { c.SSE.Mode = "AES" }, "sse.mode"},
		{"zero-bomb-compressed", func(c *config.Config) { c.BombDefence.MaxCompressedSizeBytes = 0 }, "maxCompressedSizeBytes"},
		{"zero-bomb-extracted", func(c *config.Config) { c.BombDefence.MaxExtractedSizeBytes = 0 }, "maxExtractedSizeBytes"},
		{"zero-bomb-ratio", func(c *config.Config) { c.BombDefence.MaxCompressionRatio = 0 }, "maxCompressionRatio"},
		{"zero-bomb-entries", func(c *config.Config) { c.BombDefence.MaxEntryCount = 0 }, "maxEntryCount"},
		{"zero-bomb-depth", func(c *config.Config) { c.BombDefence.MaxDirectoryDepth = 0 }, "maxDirectoryDepth"},
		{"zero-bomb-single", func(c *config.Config) { c.BombDefence.MaxSingleFileSizeBytes = 0 }, "maxSingleFileSizeBytes"},
		{"zero-bomb-duration", func(c *config.Config) { c.BombDefence.MaxExtractionDurationSec = 0 }, "maxExtractionDurationSec"},
		{"zero-bomb-total-declared", func(c *config.Config) { c.BombDefence.MaxTotalDeclaredUncompressedBytes = 0 }, "maxTotalDeclaredUncompressedBytes"},
		{"zero-stream-buffer", func(c *config.Config) { c.Streaming.MaxInMemoryBufferBytes = 0 }, "maxInMemoryBufferBytes"},
		{"low-multipart", func(c *config.Config) { c.Streaming.MultipartThresholdBytes = 1024 }, "multipartThresholdBytes"},
		{"zero-retry-attempts", func(c *config.Config) { c.Retry.MaxAttempts = 0 }, "retry.maxAttempts"},
		{"zero-retry-base", func(c *config.Config) { c.Retry.BackoffBaseMillis = 0 }, "backoffBaseMillis"},
		{"low-retry-factor", func(c *config.Config) { c.Retry.BackoffFactor = 0.5 }, "backoffFactor"},
		{"bad-jitter", func(c *config.Config) { c.Retry.JitterFraction = 2 }, "jitterFraction"},
		{"zero-sqs-heartbeat", func(c *config.Config) { c.SQS.HeartbeatIntervalSec = 0 }, "heartbeatIntervalSec"},
		{"zero-sqs-inflight", func(c *config.Config) { c.SQS.MaxInFlight = 0 }, "maxInFlight"},
		{"zero-sqs-drain", func(c *config.Config) { c.SQS.GracefulShutdownTimeoutSec = 0 }, "gracefulShutdownTimeoutSec"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mut(&c)
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestValidate_SSEKMSRequiresKey(t *testing.T) {
	c := config.Config{
		Infra:   config.InfraConfig{QueueURL: "x", StagingBucket: "y", DynamoTable: "z"},
		HTTP:    config.HTTPConfig{Port: 8080},
		Logging: config.LoggingConfig{Format: "json", Level: "info"},
		SSE:     config.SSEConfig{Mode: "SSE-KMS"}, // no KMSKeyID
		BombDefence: config.BombDefenceConfig{
			MaxCompressedSizeBytes: 1, MaxExtractedSizeBytes: 1,
			MaxCompressionRatio: 1, MaxEntryCount: 1, MaxDirectoryDepth: 1,
			MaxSingleFileSizeBytes: 1, MaxExtractionDurationSec: 1,
			MaxTotalDeclaredUncompressedBytes: 1,
		},
		Streaming: config.StreamingConfig{MaxInMemoryBufferBytes: 1, MultipartThresholdBytes: 5 * 1024 * 1024},
		Retry:     config.RetryConfig{MaxAttempts: 1, BackoffBaseMillis: 1, BackoffFactor: 1, JitterFraction: 0},
		SQS:       config.SQSConfig{HeartbeatIntervalSec: 1, MaxInFlight: 1, GracefulShutdownTimeoutSec: 1, VisibilityTimeoutSec: 1},
	}
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kmsKeyId")
}
