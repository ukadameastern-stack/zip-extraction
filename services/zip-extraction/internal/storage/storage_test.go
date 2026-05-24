package storage_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/storage"
)

func TestDetectMIME_Examples(t *testing.T) {
	cases := []struct {
		name     string
		peek     []byte
		fileName string
		want     string
	}{
		{"png_sniff", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, "x.png", "image/png"},
		{"pdf_sniff", []byte("%PDF-1.4"), "x.pdf", "application/pdf"},
		{"sniff_octet_ext_fallback", []byte{0, 0, 0, 0, 0, 0}, "doc.txt", "text/plain"},
		{"all_unknown", []byte{0, 0, 0, 0, 0, 0}, "noext", "application/octet-stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := storage.DetectMIME(tc.peek, tc.fileName)
			// The text/plain MIME may include "; charset=utf-8" from mime stdlib.
			assert.True(t, strings.HasPrefix(got, tc.want), "got=%q want-prefix=%q", got, tc.want)
		})
	}
}

func TestPeek_ReturnsBytesWithoutAdvancing(t *testing.T) {
	body := strings.NewReader("hello, world")
	p, err := storage.Peek(body, 5)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "hello", string(p.Peek))

	// Read all bytes from Rebuilt — must yield the full original "hello, world".
	out := make([]byte, 32)
	n, _ := io.ReadFull(p.Rebuilt, out)
	// io.ReadFull returns the bytes read; "hello, world" is 12 bytes.
	assert.Equal(t, "hello, world", string(out[:12]))
	_ = n
}

func TestPeek_ShortStream(t *testing.T) {
	body := strings.NewReader("hi")
	p, err := storage.Peek(body, 10)
	require.NoError(t, err)
	assert.Equal(t, "hi", string(p.Peek))
}

// --- Adapter.Download + Upload tests using fakes ---

type fakeS3API struct {
	getBody  []byte
	getErr   error
	putErr   error
	gotPut   *s3.PutObjectInput
	gotGet   *s3.GetObjectInput
	putCalls int
}

func (f *fakeS3API) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.gotGet = in
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(string(f.getBody))),
	}
	size := int64(len(f.getBody))
	out.ContentLength = &size
	return out, nil
}

func (f *fakeS3API) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.gotPut = in
	f.putCalls++
	if f.putErr != nil {
		return nil, f.putErr
	}
	// Consume the body so the test sees uploaded length.
	if in.Body != nil {
		_, _ = io.Copy(io.Discard, in.Body)
	}
	return &s3.PutObjectOutput{}, nil
}

type fakeUploader struct {
	err      error
	gotInput *s3.PutObjectInput
	calls    int
}

func (f *fakeUploader) Upload(ctx context.Context, in *s3.PutObjectInput, _ ...func(*manager.Uploader)) (*manager.UploadOutput, error) {
	f.gotInput = in
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if in.Body != nil {
		_, _ = io.Copy(io.Discard, in.Body)
	}
	return &manager.UploadOutput{}, nil
}

func TestDownload_HappyPath(t *testing.T) {
	api := &fakeS3API{getBody: []byte("hello-archive-bytes")}
	a := storage.NewAdapter(api, &fakeUploader{}, storage.Config{MultipartThresholdBytes: 5 * 1024 * 1024})
	rc, size, err := a.Download(context.Background(), "bucket", "uploads/x.zip")
	require.NoError(t, err)
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	assert.Equal(t, "hello-archive-bytes", string(body))
	assert.Equal(t, int64(len("hello-archive-bytes")), size)
	assert.Equal(t, "bucket", aws.ToString(api.gotGet.Bucket))
	assert.Equal(t, "uploads/x.zip", aws.ToString(api.gotGet.Key))
}

func TestDownload_GetErrorWraps(t *testing.T) {
	api := &fakeS3API{getErr: throttlingErr{}}
	a := storage.NewAdapter(api, &fakeUploader{}, storage.Config{})
	_, _, err := a.Download(context.Background(), "b", "k")
	require.Error(t, err)
	te, ok := extraction.IsTransient(err)
	require.True(t, ok)
	assert.Equal(t, extraction.TransientClassThrottling, te.Class)
}

// Upload always routes through s3manager.Uploader (it handles non-seekable streams
// and switches to multipart internally above its own threshold).
func TestUpload_SmallBody(t *testing.T) {
	api := &fakeS3API{}
	up := &fakeUploader{}
	a := storage.NewAdapter(api, up, storage.Config{MultipartThresholdBytes: 5 * 1024 * 1024, SSEMode: "SSE-S3"})

	body := strings.NewReader("small body")
	err := a.Upload(context.Background(), "bucket", "input/x/0001-a.txt", body, int64(len("small body")), "text/plain")
	require.NoError(t, err)
	assert.Equal(t, 1, up.calls)
	assert.Equal(t, 0, api.putCalls, "direct PutObject must NOT be used (non-seekable bodies)")
	assert.Equal(t, s3types.ServerSideEncryptionAes256, up.gotInput.ServerSideEncryption)
	assert.Equal(t, "text/plain", aws.ToString(up.gotInput.ContentType))
}

func TestUpload_LargeBody(t *testing.T) {
	api := &fakeS3API{}
	up := &fakeUploader{}
	a := storage.NewAdapter(api, up, storage.Config{MultipartThresholdBytes: 5 * 1024 * 1024})
	err := a.Upload(context.Background(), "bucket", "input/x/0002-big.bin", strings.NewReader("data"), 10*1024*1024, "")
	require.NoError(t, err)
	assert.Equal(t, 1, up.calls)
	assert.Equal(t, 0, api.putCalls)
}

func TestUpload_UnknownSize(t *testing.T) {
	api := &fakeS3API{}
	up := &fakeUploader{}
	a := storage.NewAdapter(api, up, storage.Config{MultipartThresholdBytes: 5 * 1024 * 1024})
	err := a.Upload(context.Background(), "bucket", "input/x/0003-unknown.bin", strings.NewReader("x"), -1, "")
	require.NoError(t, err)
	assert.Equal(t, 1, up.calls)
}

func TestUpload_SSEKMS(t *testing.T) {
	up := &fakeUploader{}
	a := storage.NewAdapter(&fakeS3API{}, up, storage.Config{
		MultipartThresholdBytes: 5 * 1024 * 1024,
		SSEMode:                 "SSE-KMS",
		SSEKMSKeyID:             "arn:aws:kms:eu-west-1:111:key/abc",
	})
	err := a.Upload(context.Background(), "bucket", "k", strings.NewReader("x"), 1, "application/octet-stream")
	require.NoError(t, err)
	assert.Equal(t, s3types.ServerSideEncryptionAwsKms, up.gotInput.ServerSideEncryption)
	assert.Equal(t, "arn:aws:kms:eu-west-1:111:key/abc", aws.ToString(up.gotInput.SSEKMSKeyId))
}

func TestUpload_UploaderErrorWraps(t *testing.T) {
	up := &fakeUploader{err: throttlingErr{}}
	a := storage.NewAdapter(&fakeS3API{}, up, storage.Config{MultipartThresholdBytes: 5 * 1024 * 1024})
	err := a.Upload(context.Background(), "bucket", "k", strings.NewReader("x"), 10*1024*1024, "")
	require.Error(t, err)
	te, ok := extraction.IsTransient(err)
	require.True(t, ok)
	assert.Equal(t, extraction.TransientClassThrottling, te.Class)
}

// throttlingErr satisfies smithy.APIError so retry.AsTransient classifies it.
type throttlingErr struct{}

func (throttlingErr) Error() string                  { return "SlowDown" }
func (throttlingErr) ErrorCode() string              { return "SlowDown" }
func (throttlingErr) ErrorMessage() string           { return "slow" }
func (throttlingErr) ErrorFault() smithy.ErrorFault  { return smithy.FaultServer }
