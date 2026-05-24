// Package config loads service configuration from environment variables
// (infrastructure values per FR-14.1) and a YAML file at CONFIG_PATH
// (tunable limits per FR-14.2). It enforces strict-decode (rejects unknown
// keys) and fail-fast validation per BR-LOG-001 / NFR-Z-050 / SECURITY-15.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config aggregates all runtime configuration.
type Config struct {
	Infra       InfraConfig
	HTTP        HTTPConfig
	Logging     LoggingConfig
	SSE         SSEConfig
	BombDefence BombDefenceConfig `yaml:"bombDefence"`
	Streaming   StreamingConfig   `yaml:"streaming"`
	Retry       RetryConfig       `yaml:"retry"`
	SQS         SQSConfig         `yaml:"sqs"`
}

// InfraConfig holds environment-injected infrastructure references (FR-14.1).
type InfraConfig struct {
	Region         string
	AWSEndpointURL string
	QueueURL       string
	StagingBucket  string
	DynamoTable    string
}

// HTTPConfig configures the operational HTTP server.
type HTTPConfig struct {
	Port int
}

// LoggingConfig configures the structured logger.
type LoggingConfig struct {
	Format string
	Level  string
}

// SSEConfig configures the S3 server-side-encryption mode (Q6 of NFR design).
type SSEConfig struct {
	Mode     string // "SSE-S3" | "SSE-KMS"
	KMSKeyID string
}

// BombDefenceConfig holds the 10-rule defence thresholds (FR-7).
type BombDefenceConfig struct {
	MaxCompressedSizeBytes   int64   `yaml:"maxCompressedSizeBytes"`
	MaxExtractedSizeBytes    int64   `yaml:"maxExtractedSizeBytes"`
	MaxCompressionRatio      float64 `yaml:"maxCompressionRatio"`
	MaxEntryCount            int     `yaml:"maxEntryCount"`
	MaxDirectoryDepth        int     `yaml:"maxDirectoryDepth"`
	MaxSingleFileSizeBytes   int64   `yaml:"maxSingleFileSizeBytes"`
	MaxExtractionDurationSec int     `yaml:"maxExtractionDurationSec"`
}

// StreamingConfig holds streaming-I/O constants (NFR-Z-014).
type StreamingConfig struct {
	MaxInMemoryBufferBytes  int64 `yaml:"maxInMemoryBufferBytes"`
	MultipartThresholdBytes int64 `yaml:"multipartThresholdBytes"`
}

// RetryConfig holds the classifier-driven retry parameters (FR-12).
type RetryConfig struct {
	MaxAttempts       int     `yaml:"maxAttempts"`
	BackoffBaseMillis int     `yaml:"backoffBaseMillis"`
	BackoffFactor     float64 `yaml:"backoffFactor"`
	JitterFraction    float64 `yaml:"jitterFraction"`
}

// SQSConfig holds receive-loop + heartbeat + drain tunables (FR-9, Q7 of app design).
type SQSConfig struct {
	HeartbeatIntervalSec       int `yaml:"heartbeatIntervalSec"`
	MaxInFlight                int `yaml:"maxInFlight"`
	GracefulShutdownTimeoutSec int `yaml:"gracefulShutdownTimeoutSec"`
	VisibilityTimeoutSec       int `yaml:"visibilityTimeoutSec"`
}

// Load reads env vars + parses YAML at $CONFIG_PATH and returns a validated
// Config. Failure causes are wrapped with a "config: " prefix.
func Load() (Config, error) {
	var c Config

	c.Infra = InfraConfig{
		Region:         getEnvDefault("AWS_REGION", "eu-west-1"),
		AWSEndpointURL: os.Getenv("AWS_ENDPOINT_URL"),
		QueueURL:       os.Getenv("QUEUE_URL"),
		StagingBucket:  os.Getenv("STAGING_BUCKET"),
		DynamoTable:    getEnvDefault("DYNAMO_TABLE", "pipeline_files"),
	}

	port, err := parseEnvInt("HTTP_PORT", 8080)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	c.HTTP = HTTPConfig{Port: port}

	c.Logging = LoggingConfig{
		Format: getEnvDefault("LOG_FORMAT", "json"),
		Level:  getEnvDefault("LOG_LEVEL", "info"),
	}

	c.SSE = SSEConfig{
		Mode:     getEnvDefault("SSE_MODE", "SSE-S3"),
		KMSKeyID: os.Getenv("SSE_KMS_KEY_ID"),
	}

	yamlPath := getEnvDefault("CONFIG_PATH", "/etc/zip-extraction/config.yaml")
	if err := loadYAML(yamlPath, &c); err != nil {
		return Config{}, fmt.Errorf("config: load yaml %s: %w", yamlPath, err)
	}

	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config: validate: %w", err)
	}
	return c, nil
}

// Validate enforces range and consistency constraints (NFR-Z-050).
//
//nolint:gocyclo // exhaustive validation is intentionally flat
func (c Config) Validate() error {
	if c.Infra.QueueURL == "" {
		return fmt.Errorf("infra.queueURL: required (env QUEUE_URL)")
	}
	if c.Infra.StagingBucket == "" {
		return fmt.Errorf("infra.stagingBucket: required (env STAGING_BUCKET)")
	}
	if c.Infra.DynamoTable == "" {
		return fmt.Errorf("infra.dynamoTable: required (env DYNAMO_TABLE)")
	}
	if c.HTTP.Port <= 0 || c.HTTP.Port > 65535 {
		return fmt.Errorf("http.port: %d out of range 1..65535", c.HTTP.Port)
	}

	switch strings.ToLower(c.Logging.Format) {
	case "json", "console":
	default:
		return fmt.Errorf("logging.format: %q (want json|console)", c.Logging.Format)
	}

	switch c.SSE.Mode {
	case "SSE-S3":
	case "SSE-KMS":
		if c.SSE.KMSKeyID == "" {
			return fmt.Errorf("sse.kmsKeyId: required when sse.mode=SSE-KMS")
		}
	default:
		return fmt.Errorf("sse.mode: %q (want SSE-S3|SSE-KMS)", c.SSE.Mode)
	}

	if c.BombDefence.MaxCompressedSizeBytes <= 0 {
		return fmt.Errorf("bombDefence.maxCompressedSizeBytes: %d must be > 0", c.BombDefence.MaxCompressedSizeBytes)
	}
	if c.BombDefence.MaxExtractedSizeBytes <= 0 {
		return fmt.Errorf("bombDefence.maxExtractedSizeBytes: %d must be > 0", c.BombDefence.MaxExtractedSizeBytes)
	}
	if c.BombDefence.MaxCompressionRatio <= 0 {
		return fmt.Errorf("bombDefence.maxCompressionRatio: %v must be > 0", c.BombDefence.MaxCompressionRatio)
	}
	if c.BombDefence.MaxEntryCount <= 0 {
		return fmt.Errorf("bombDefence.maxEntryCount: %d must be > 0", c.BombDefence.MaxEntryCount)
	}
	if c.BombDefence.MaxDirectoryDepth <= 0 {
		return fmt.Errorf("bombDefence.maxDirectoryDepth: %d must be > 0", c.BombDefence.MaxDirectoryDepth)
	}
	if c.BombDefence.MaxSingleFileSizeBytes <= 0 {
		return fmt.Errorf("bombDefence.maxSingleFileSizeBytes: %d must be > 0", c.BombDefence.MaxSingleFileSizeBytes)
	}
	if c.BombDefence.MaxExtractionDurationSec <= 0 {
		return fmt.Errorf("bombDefence.maxExtractionDurationSec: %d must be > 0", c.BombDefence.MaxExtractionDurationSec)
	}

	if c.Streaming.MaxInMemoryBufferBytes <= 0 {
		return fmt.Errorf("streaming.maxInMemoryBufferBytes: %d must be > 0", c.Streaming.MaxInMemoryBufferBytes)
	}
	const minMultipartThreshold = 5 * 1024 * 1024 // S3 multipart minimum part size
	if c.Streaming.MultipartThresholdBytes < minMultipartThreshold {
		return fmt.Errorf("streaming.multipartThresholdBytes: %d must be >= %d (S3 multipart min)",
			c.Streaming.MultipartThresholdBytes, minMultipartThreshold)
	}

	if c.Retry.MaxAttempts <= 0 {
		return fmt.Errorf("retry.maxAttempts: %d must be > 0", c.Retry.MaxAttempts)
	}
	if c.Retry.BackoffBaseMillis <= 0 {
		return fmt.Errorf("retry.backoffBaseMillis: %d must be > 0", c.Retry.BackoffBaseMillis)
	}
	if c.Retry.BackoffFactor < 1.0 {
		return fmt.Errorf("retry.backoffFactor: %v must be >= 1.0", c.Retry.BackoffFactor)
	}
	if c.Retry.JitterFraction < 0 || c.Retry.JitterFraction > 1 {
		return fmt.Errorf("retry.jitterFraction: %v must be in [0, 1]", c.Retry.JitterFraction)
	}

	if c.SQS.HeartbeatIntervalSec <= 0 {
		return fmt.Errorf("sqs.heartbeatIntervalSec: %d must be > 0", c.SQS.HeartbeatIntervalSec)
	}
	if c.SQS.MaxInFlight <= 0 {
		return fmt.Errorf("sqs.maxInFlight: %d must be > 0", c.SQS.MaxInFlight)
	}
	if c.SQS.GracefulShutdownTimeoutSec <= 0 {
		return fmt.Errorf("sqs.gracefulShutdownTimeoutSec: %d must be > 0", c.SQS.GracefulShutdownTimeoutSec)
	}
	if c.SQS.VisibilityTimeoutSec <= 0 {
		c.SQS.VisibilityTimeoutSec = 300 // default per §1 input spec — but caller already populated; fall back
	}
	return nil
}

func loadYAML(path string, c *Config) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func getEnvDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func parseEnvInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer: %w", key, v, err)
	}
	return n, nil
}
