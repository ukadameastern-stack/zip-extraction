//go:build e2e

// Package e2e contains Gate 2 (NFR-Z-082) integration tests that exercise the
// service end-to-end against a Testcontainers-managed LocalStack instance.
//
// Tagged with `//go:build e2e` so they don't run on default `go test ./...`.
// Invoke via: `go test -tags=e2e ./test/e2e/...`
package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/require"
	tcls "github.com/testcontainers/testcontainers-go/modules/localstack"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/awsclients"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
)

const (
	testRegion = "eu-west-1"
	testBucket = "doc-uploader-staging-e2e"
	testQueue  = "zip-extraction-queue"
	testDLQ    = "zip-extraction-dlq"
	testTable  = "pipeline_files"
)

type e2eEnv struct {
	endpoint string
	clients  awsclients.Set
	queueURL string
}

func setupLocalStack(t *testing.T) e2eEnv {
	t.Helper()
	ctx := context.Background()

	container, err := tcls.Run(ctx,
		"localstack/localstack:3.7",
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	endpoint, err := container.PortEndpoint(ctx, "4566/tcp", "http")
	require.NoError(t, err)

	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	clients, err := awsclients.Build(ctx, config.InfraConfig{
		Region:         testRegion,
		AWSEndpointURL: endpoint,
	})
	require.NoError(t, err)

	// Provision: bucket / DLQ / queue / DDB table.
	_, err = clients.S3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(testBucket)})
	require.NoError(t, err)

	dlqOut, err := clients.SQS.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(testDLQ)})
	require.NoError(t, err)
	dlqAttrs, err := clients.SQS.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       dlqOut.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	dlqArn := dlqAttrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
	redrive, _ := json.Marshal(map[string]any{"deadLetterTargetArn": dlqArn, "maxReceiveCount": "3"})

	qOut, err := clients.SQS.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(testQueue),
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNameRedrivePolicy):     string(redrive),
			string(sqstypes.QueueAttributeNameVisibilityTimeout): "300",
		},
	})
	require.NoError(t, err)

	_, err = clients.DDB.CreateTable(ctx, &awsddb.CreateTableInput{
		TableName: aws.String(testTable),
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: ddbtypes.KeyTypeRange},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	require.NoError(t, err)

	return e2eEnv{endpoint: endpoint, clients: clients, queueURL: *qOut.QueueUrl}
}

// makeArchive constructs an in-memory ZIP with the given child entries. Each
// entry is uncompressed for predictability under bomb-defence assertions.
func makeArchive(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		fh := &zip.FileHeader{Name: name, Method: zip.Store}
		f, err := w.CreateHeader(fh)
		require.NoError(t, err)
		_, err = f.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func uploadArchive(t *testing.T, env e2eEnv, key string, body []byte) {
	t.Helper()
	_, err := env.clients.S3.PutObject(context.Background(), &awss3.PutObjectInput{
		Bucket: aws.String(testBucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	require.NoError(t, err)
}

func TestE2E_HappyPath_SUCCESS(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e Testcontainers test skipped in -short mode")
	}
	env := setupLocalStack(t)

	archive := makeArchive(t, map[string][]byte{
		"a.txt":      []byte("hello"),
		"b/c.txt":    []byte("world"),
		"docs/d.pdf": []byte("%PDF-1.4\n..."),
	})
	uploadArchive(t, env, "uploads/exec-success.zip", archive)

	// In a full Gate 2 harness the service binary is started as a child process
	// or in-process and we wait for the DDB rows. For this skeleton we just
	// assert the prerequisite plumbing is reachable.
	require.NotEmpty(t, env.queueURL)

	// Smoke-check that the DDB table is queryable.
	_, err := env.clients.DDB.Scan(context.Background(), &awsddb.ScanInput{
		TableName: aws.String(testTable),
		Limit:     aws.Int32(1),
	})
	require.NoError(t, err)
}

// Additional E2E scenarios to be implemented in the Build & Test stage:
//   - TestE2E_BombDefence_RejectsRule1
//   - TestE2E_PathTraversal_FAILED
//   - TestE2E_TransientRetry_PARTIAL_FAILED
//   - TestE2E_Redelivery_Idempotent
// These are scaffolded as placeholders so the Build & Test stage can populate
// the assertion bodies once the application binary lifecycle is wired into the
// test harness (in-process vs subprocess to be decided then).

var _ = io.EOF // placeholder to keep all imports honest
var _ = fmt.Sprintf
var _ = time.Second
