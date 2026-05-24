package sqs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMessage_HappyPath(t *testing.T) {
	body := `{
		"pipelineExecutionId":"exec-1",
		"tenantId":"tenant-1",
		"documentId":"doc-1",
		"sourceBucket":"bucket",
		"sourceKey":"uploads/x.zip",
		"correlationId":"corr-1"
	}`
	msg, err := parseMessage(&body)
	require.NoError(t, err)
	assert.Equal(t, "exec-1", msg.PipelineExecutionID)
}

func TestParseMessage_RejectsMissingFields(t *testing.T) {
	cases := []string{
		`{}`,
		`{"pipelineExecutionId":"x"}`,
		`{"pipelineExecutionId":"x","tenantId":"t","documentId":"d","sourceBucket":"b","sourceKey":"k"}`, // missing correlationId
	}
	for _, body := range cases {
		body := body
		t.Run(body, func(t *testing.T) {
			_, err := parseMessage(&body)
			require.Error(t, err)
		})
	}
}

func TestParseMessage_RejectsMalformedJSON(t *testing.T) {
	bad := `{not-json`
	_, err := parseMessage(&bad)
	require.Error(t, err)
}
