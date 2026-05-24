// Package awsclients constructs AWS SDK v2 clients (SQS, S3, DynamoDB, S3
// multipart uploader) honouring optional LocalStack endpoint override per
// FR-15.1. Clients are constructed once per pod and shared across goroutines
// (NFR-Z-013 / pattern §3.4 — singleton clients).
//
// Adaptive retry mode (aws.RetryModeAdaptive) is enabled at the SDK layer; the
// application-level classifier-driven retry in internal/retry layers on top of
// this and is the authoritative decision-maker for application retries
// (pattern §1.6 — no explicit circuit breaker).
package awsclients

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	cfgpkg "github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
)

// Set holds the singleton AWS clients constructed for this pod.
type Set struct {
	SQS        *sqs.Client
	S3         *s3.Client
	DDB        *dynamodb.Client
	S3Uploader *manager.Uploader
}

// Build constructs the client Set from the given InfraConfig. If
// cfg.AWSEndpointURL is non-empty, all clients route through it (LocalStack).
func Build(ctx context.Context, cfg cfgpkg.InfraConfig) (Set, error) {
	if cfg.Region == "" {
		return Set{}, errors.New("awsclients: region is required")
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithRetryMode(aws.RetryModeAdaptive),
		awsconfig.WithRetryMaxAttempts(3),
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return Set{}, fmt.Errorf("awsclients: load default config: %w", err)
	}

	// Per-service client options. LocalStack requires path-style S3 + endpoint override.
	s3Opts := func(o *s3.Options) {
		if cfg.AWSEndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.AWSEndpointURL)
			o.UsePathStyle = true
		}
	}
	sqsOpts := func(o *sqs.Options) {
		if cfg.AWSEndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.AWSEndpointURL)
		}
	}
	ddbOpts := func(o *dynamodb.Options) {
		if cfg.AWSEndpointURL != "" {
			o.BaseEndpoint = aws.String(cfg.AWSEndpointURL)
		}
	}

	s3Client := s3.NewFromConfig(awsCfg, s3Opts)

	return Set{
		SQS:        sqs.NewFromConfig(awsCfg, sqsOpts),
		S3:         s3Client,
		DDB:        dynamodb.NewFromConfig(awsCfg, ddbOpts),
		S3Uploader: manager.NewUploader(s3Client),
	}, nil
}
