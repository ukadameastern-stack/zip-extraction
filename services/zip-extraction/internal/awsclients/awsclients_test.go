package awsclients_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/awsclients"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/config"
)

func TestBuild_RequiresRegion(t *testing.T) {
	_, err := awsclients.Build(context.Background(), config.InfraConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")
}

func TestBuild_NoEndpointOverride(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")

	set, err := awsclients.Build(context.Background(), config.InfraConfig{Region: "eu-west-1"})
	require.NoError(t, err)
	require.NotNil(t, set.SQS)
	require.NotNil(t, set.S3)
	require.NotNil(t, set.DDB)
	require.NotNil(t, set.S3Uploader)
}

func TestBuild_LocalStackEndpoint(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
	t.Setenv("AWS_CONFIG_FILE", "/dev/null")

	set, err := awsclients.Build(context.Background(), config.InfraConfig{
		Region:         "eu-west-1",
		AWSEndpointURL: "http://localhost:4566",
	})
	require.NoError(t, err)
	assert.NotNil(t, set.SQS)
	assert.NotNil(t, set.S3)
	assert.NotNil(t, set.DDB)
	assert.NotNil(t, set.S3Uploader)
}
