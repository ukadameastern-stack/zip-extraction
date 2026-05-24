// Package dynamodb is the DynamoDB adapter implementing extraction.Recorder.
// Writes use conditional PutItem with attribute_not_exists(pk) per
// BR-IDEMPOTENCY-002; ConditionalCheckFailedException is treated as an
// idempotent re-delivery and converted to nil (with a redelivery_skips_total
// metric increment by the caller).
package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/retry"
)

// DDBAPI is the minimum SDK surface the adapter uses.
type DDBAPI interface {
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

// Adapter implements extraction.Recorder.
type Adapter struct {
	api       DDBAPI
	tableName string
	onSkip    func() // optional callback when CCFE indicates redelivery (metrics hook)
}

// NewAdapter constructs an Adapter. onSkip is invoked once per idempotency-skip event.
func NewAdapter(api DDBAPI, tableName string, onSkip func()) *Adapter {
	return &Adapter{api: api, tableName: tableName, onSkip: onSkip}
}

// RecordEntry implements extraction.Recorder.RecordEntry per BR-DDB-002.
func (a *Adapter) RecordEntry(ctx context.Context, rec extraction.PipelineFile) error {
	item, err := Marshal(rec)
	if err != nil {
		return err
	}
	_, err = a.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(a.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err == nil {
		return nil
	}
	// ConditionalCheckFailedException → idempotent re-delivery.
	var cce *ddbtypes.ConditionalCheckFailedException
	if errors.As(err, &cce) {
		if a.onSkip != nil {
			a.onSkip()
		}
		return nil
	}
	return retry.AsTransient(fmt.Errorf("dynamodb: putItem %s pk=%s sk=%s: %w", a.tableName, rec.PK, rec.SK, err))
}

// Marshal converts an extraction.PipelineFile to a DynamoDB AttributeValue map.
// Exported for PBT-02 round-trip property.
func Marshal(rec extraction.PipelineFile) (map[string]ddbtypes.AttributeValue, error) {
	if rec.PK == "" || rec.SK == "" {
		return nil, fmt.Errorf("dynamodb: marshal requires non-empty PK and SK")
	}
	m := map[string]ddbtypes.AttributeValue{
		"pk":            &ddbtypes.AttributeValueMemberS{Value: rec.PK},
		"sk":            &ddbtypes.AttributeValueMemberS{Value: rec.SK},
		"documentId":    &ddbtypes.AttributeValueMemberS{Value: rec.DocumentID},
		"sourceArchive": &ddbtypes.AttributeValueMemberS{Value: rec.SourceArchive},
		"childKey":      &ddbtypes.AttributeValueMemberS{Value: rec.ChildKey},
		"mimeType":      &ddbtypes.AttributeValueMemberS{Value: rec.MimeType},
		"status":        &ddbtypes.AttributeValueMemberS{Value: rec.Status},
		"sizeBytes":     &ddbtypes.AttributeValueMemberN{Value: fmt.Sprintf("%d", rec.SizeBytes)},
		"recordedAt":    &ddbtypes.AttributeValueMemberS{Value: rec.RecordedAt.UTC().Format(time.RFC3339Nano)},
	}
	if rec.FailureReason != "" {
		m["failureReason"] = &ddbtypes.AttributeValueMemberS{Value: rec.FailureReason}
	}
	if rec.FailureDetail != "" {
		m["failureDetail"] = &ddbtypes.AttributeValueMemberS{Value: rec.FailureDetail}
	}
	return m, nil
}

// Unmarshal converts a DynamoDB AttributeValue map back to extraction.PipelineFile.
// Inverse of Marshal — supports the PBT-02 round-trip property.
func Unmarshal(av map[string]ddbtypes.AttributeValue) (extraction.PipelineFile, error) {
	var rec extraction.PipelineFile
	rec.PK = getString(av, "pk")
	rec.SK = getString(av, "sk")
	rec.DocumentID = getString(av, "documentId")
	rec.SourceArchive = getString(av, "sourceArchive")
	rec.ChildKey = getString(av, "childKey")
	rec.MimeType = getString(av, "mimeType")
	rec.Status = getString(av, "status")
	if n, err := parseInt64(av, "sizeBytes"); err == nil {
		rec.SizeBytes = n
	}
	rec.FailureReason = getString(av, "failureReason")
	rec.FailureDetail = getString(av, "failureDetail")
	if s := getString(av, "recordedAt"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			rec.RecordedAt = t
		}
	}
	if rec.PK == "" || rec.SK == "" {
		return extraction.PipelineFile{}, fmt.Errorf("dynamodb: unmarshal missing pk/sk")
	}
	return rec, nil
}

func getString(av map[string]ddbtypes.AttributeValue, k string) string {
	if v, ok := av[k]; ok {
		if s, ok := v.(*ddbtypes.AttributeValueMemberS); ok {
			return s.Value
		}
	}
	return ""
}

func parseInt64(av map[string]ddbtypes.AttributeValue, k string) (int64, error) {
	v, ok := av[k]
	if !ok {
		return 0, errors.New("missing")
	}
	n, ok := v.(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return 0, errors.New("not numeric")
	}
	var out int64
	_, err := fmt.Sscanf(n.Value, "%d", &out)
	return out, err
}
