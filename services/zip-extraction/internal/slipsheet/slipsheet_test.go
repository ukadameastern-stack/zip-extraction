package slipsheet_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/slipsheet"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/test/generators"
)

func TestBuild_SuccessShape(t *testing.T) {
	entries := []extraction.EntryOutcome{
		{Index: 1, ChildKey: "input/x/0001-a.pdf", Status: extraction.EntryStatusUploaded, SizeBytes: 100},
		{Index: 2, ChildKey: "input/x/0002-b.pdf", Status: extraction.EntryStatusUploaded, SizeBytes: 200},
	}
	ss := slipsheet.Build("x", "uploads/archive.zip", extraction.StatusSuccess, entries, "", "", time.Unix(0, 0))
	assert.Equal(t, slipsheet.SlipsheetType, ss.Type)
	assert.Equal(t, "SUCCESS", ss.Status)
	assert.Empty(t, ss.FailureReason)
	assert.Empty(t, ss.FailureDetail)
	assert.Equal(t, 2, ss.ChildCount)
	assert.Len(t, ss.Children, 2)
}

func TestBuild_FailedStubHasEmptyChildren(t *testing.T) {
	ss := slipsheet.Build("x", "uploads/archive.zip", extraction.StatusFailed, nil, "bomb-defence rule 1", "rule 1: compressed archive size exceeds the configured cap", time.Unix(0, 0))
	assert.Equal(t, "FAILED", ss.Status)
	assert.Equal(t, "bomb-defence rule 1", ss.FailureReason)
	assert.Contains(t, ss.FailureDetail, "rule 1")
	assert.Equal(t, 0, ss.ChildCount)
	assert.NotNil(t, ss.Children) // BR-SLIP-004 — empty array, not null
}

// PBT-02 round-trip: Unmarshal(Marshal(ss)) == ss.
func TestPropertyRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		entries := make([]extraction.EntryOutcome, rapid.IntRange(0, 5).Draw(t, "n"))
		for i := range entries {
			entries[i] = generators.EntryOutcome(0.3).Draw(t, "outcome")
			entries[i].Index = i + 1
		}
		// derive status deterministically from entries so the round-trip is meaningful
		status := extraction.StatusSuccess
		if len(entries) > 0 {
			ok, fail := 0, 0
			for _, e := range entries {
				if e.Status == extraction.EntryStatusUploaded {
					ok++
				} else {
					fail++
				}
			}
			switch {
			case ok == len(entries):
				status = extraction.StatusSuccess
			case ok > 0 && fail > 0:
				status = extraction.StatusPartialFailed
			default:
				status = extraction.StatusFailed
			}
		}
		ss := slipsheet.Build("x", "uploads/archive.zip", status, entries, "", "", time.Unix(0, 0))

		b, err := slipsheet.Marshal(ss)
		require.NoError(t, err)
		got, err := slipsheet.Unmarshal(b)
		require.NoError(t, err)
		assert.Equal(t, ss, got)
	})
}

func TestWriter_WritesViaUploader(t *testing.T) {
	uploader := &fakeUploader{}
	w := slipsheet.NewWriter(uploader, "bucket", "slipsheets/")
	err := w.Write(context.Background(), "exec-1", "uploads/x.zip", extraction.StatusSuccess, nil, "", "")
	require.NoError(t, err)
	require.Len(t, uploader.calls, 1)
	assert.Equal(t, "bucket", uploader.calls[0].bucket)
	assert.Equal(t, "slipsheets/exec-1.json", uploader.calls[0].key)
	assert.Equal(t, "application/json", uploader.calls[0].contentType)
}

type fakeUploader struct {
	calls []uploadCall
	err   error
}

type uploadCall struct{ bucket, key, contentType string }

func (f *fakeUploader) Upload(ctx context.Context, bucket, key string, body io.Reader, sizeHint int64, contentType string) error {
	if f.err != nil {
		return f.err
	}
	_, _ = io.Copy(io.Discard, body)
	f.calls = append(f.calls, uploadCall{bucket: bucket, key: key, contentType: contentType})
	return nil
}

func TestWriter_PropagatesUploaderError(t *testing.T) {
	uploader := &fakeUploader{err: errors.New("nope")}
	w := slipsheet.NewWriter(uploader, "bucket", "slipsheets/")
	err := w.Write(context.Background(), "exec-1", "uploads/x.zip", extraction.StatusSuccess, nil, "", "")
	require.Error(t, err)
}
