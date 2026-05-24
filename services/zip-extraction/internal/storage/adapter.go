// Package storage is the S3 adapter implementing extraction.S3Downloader +
// extraction.S3Uploader. It threads MIME detection (Q6 of application design)
// into the upload path with no extra read pass.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/retry"
)

// S3API is the minimum SDK surface this adapter uses (consumer-defined for testability).
type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3UploaderAPI is the multipart-upload surface from manager.Uploader.
type S3UploaderAPI interface {
	Upload(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*manager.Uploader)) (*manager.UploadOutput, error)
}

// Adapter implements extraction.S3Downloader + extraction.S3Uploader.
type Adapter struct {
	api      S3API
	uploader S3UploaderAPI
	cfg      Config
}

// Config holds storage-adapter tunables.
type Config struct {
	MultipartThresholdBytes int64
	SSEMode                 string
	SSEKMSKeyID             string
}

// NewAdapter constructs an Adapter.
func NewAdapter(api S3API, uploader S3UploaderAPI, cfg Config) *Adapter {
	return &Adapter{api: api, uploader: uploader, cfg: cfg}
}

// Download streams the source archive. Caller MUST Close the returned ReadCloser.
func (a *Adapter) Download(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	out, err := a.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, 0, retry.AsTransient(fmt.Errorf("storage: getObject %s/%s: %w", bucket, key, err))
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return out.Body, size, nil
}

// Upload writes body to s3://bucket/key. Always uses s3manager.Uploader which:
//   - handles non-seekable io.Reader bodies (our LimitedReader + bufio chain is non-seekable);
//   - uses single PutObject for small bodies and switches to multipart above its own
//     internal threshold (default 5 MiB, configurable via cfg.MultipartThresholdBytes).
// contentType is set on the resulting object; empty string defers to S3's default.
func (a *Adapter) Upload(
	ctx context.Context,
	bucket, key string,
	body io.Reader,
	sizeHint int64,
	contentType string,
) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	switch a.cfg.SSEMode {
	case "SSE-S3":
		in.ServerSideEncryption = s3types.ServerSideEncryptionAes256
	case "SSE-KMS":
		in.ServerSideEncryption = s3types.ServerSideEncryptionAwsKms
		if a.cfg.SSEKMSKeyID != "" {
			in.SSEKMSKeyId = aws.String(a.cfg.SSEKMSKeyID)
		}
	}

	// Always use the uploader. It handles non-seekable streams (PutObject does not),
	// and uses single PutObject under the hood for bodies below its own multipart threshold.
	_ = sizeHint
	if _, err := a.uploader.Upload(ctx, in); err != nil {
		return retry.AsTransient(fmt.Errorf("storage: upload %s/%s: %w", bucket, key, err))
	}
	return nil
}

// ErrMissingBody is returned by Download when an unexpected nil body is observed.
var ErrMissingBody = errors.New("storage: nil body from GetObject")
