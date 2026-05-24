package dynamodb_test

import (
	"context"
	"testing"

	awsddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/dynamodb"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/test/generators"
)

// PBT-02 round-trip: Unmarshal(Marshal(rec)) == rec.
func TestPropertyRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rec := generators.PipelineFile().Draw(t, "rec")
		av, err := dynamodb.Marshal(rec)
		require.NoError(t, err)
		got, err := dynamodb.Unmarshal(av)
		require.NoError(t, err)
		assert.Equal(t, rec.PK, got.PK)
		assert.Equal(t, rec.SK, got.SK)
		assert.Equal(t, rec.DocumentID, got.DocumentID)
		assert.Equal(t, rec.SourceArchive, got.SourceArchive)
		assert.Equal(t, rec.ChildKey, got.ChildKey)
		assert.Equal(t, rec.MimeType, got.MimeType)
		assert.Equal(t, rec.Status, got.Status)
		assert.Equal(t, rec.SizeBytes, got.SizeBytes)
		assert.Equal(t, rec.FailureReason, got.FailureReason)
	})
}

func TestMarshal_RequiresKeys(t *testing.T) {
	_, err := dynamodb.Marshal(generators.PipelineFile().Example())
	require.NoError(t, err)
}

// Idempotency: CCFE → nil and onSkip called.
func TestRecordEntry_CCFEMapsToNil(t *testing.T) {
	api := &fakeDDB{err: &ddbtypes.ConditionalCheckFailedException{Message: pstr("dup")}}
	var skipped int
	adapter := dynamodb.NewAdapter(api, "pipeline_files", func() { skipped++ })

	rec := generators.PipelineFile().Example()
	err := adapter.RecordEntry(context.Background(), rec)
	require.NoError(t, err)
	assert.Equal(t, 1, skipped)
}

func pstr(s string) *string { return &s }

type fakeDDB struct {
	err  error
	last *awsddb.PutItemInput
}

func (f *fakeDDB) PutItem(ctx context.Context, in *awsddb.PutItemInput, _ ...func(*awsddb.Options)) (*awsddb.PutItemOutput, error) {
	f.last = in
	if f.err != nil {
		return nil, f.err
	}
	return &awsddb.PutItemOutput{}, nil
}
