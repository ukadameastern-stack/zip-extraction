#!/usr/bin/env bash
# Provision LocalStack with the AWS resources the service expects (FR-15.3).
# Idempotent — re-running is a no-op.
#
# Usage: AWS_ENDPOINT_URL=http://localhost:4566 AWS_REGION=eu-west-1 \
#        AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
#        bash deploy/bootstrap-localstack.sh

set -euo pipefail

: "${AWS_ENDPOINT_URL:=http://localhost:4566}"
: "${AWS_REGION:=eu-west-1}"

BUCKET="${STAGING_BUCKET:-doc-uploader-staging-local}"
SOURCE_BUCKET="${SOURCE_BUCKET:-doc-uploader-uploads-local}"
QUEUE_NAME="${QUEUE_NAME:-zip-extraction-queue}"
DLQ_NAME="${DLQ_NAME:-zip-extraction-dlq}"
TABLE_NAME="${DYNAMO_TABLE:-pipeline_files}"
ACCOUNT_ID="${ACCOUNT_ID:-000000000000}"

AWS_CLI=(aws --endpoint-url "$AWS_ENDPOINT_URL" --region "$AWS_REGION")

echo "==> Wait for LocalStack..."
for i in {1..30}; do
    if curl -sf "$AWS_ENDPOINT_URL/_localstack/health" > /dev/null; then
        break
    fi
    sleep 1
done

echo "==> Create S3 staging bucket ($BUCKET)"
"${AWS_CLI[@]}" s3api create-bucket \
    --bucket "$BUCKET" \
    --create-bucket-configuration "LocationConstraint=$AWS_REGION" >/dev/null 2>&1 || true

echo "==> Create S3 source bucket ($SOURCE_BUCKET)"
"${AWS_CLI[@]}" s3api create-bucket \
    --bucket "$SOURCE_BUCKET" \
    --create-bucket-configuration "LocationConstraint=$AWS_REGION" >/dev/null 2>&1 || true

echo "==> Create SQS DLQ ($DLQ_NAME)"
DLQ_URL=$("${AWS_CLI[@]}" sqs create-queue --queue-name "$DLQ_NAME" --query 'QueueUrl' --output text)
DLQ_ARN=$("${AWS_CLI[@]}" sqs get-queue-attributes --queue-url "$DLQ_URL" --attribute-names QueueArn --query 'Attributes.QueueArn' --output text)

echo "==> Create SQS main queue ($QUEUE_NAME) with redrive policy"
REDRIVE_JSON=$(printf '{"deadLetterTargetArn":"%s","maxReceiveCount":"3"}' "$DLQ_ARN")
"${AWS_CLI[@]}" sqs create-queue \
    --queue-name "$QUEUE_NAME" \
    --attributes "{\"RedrivePolicy\":\"$(printf '%s' "$REDRIVE_JSON" | sed 's/"/\\"/g')\",\"VisibilityTimeout\":\"300\"}" >/dev/null

echo "==> Create DynamoDB table ($TABLE_NAME)"
"${AWS_CLI[@]}" dynamodb create-table \
    --table-name "$TABLE_NAME" \
    --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
    --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE \
    --billing-mode PAY_PER_REQUEST >/dev/null 2>&1 || true

echo "==> Bootstrap complete."
echo "    Queue URL: $AWS_ENDPOINT_URL/$ACCOUNT_ID/$QUEUE_NAME"
echo "    Staging:   s3://$BUCKET/"
echo "    Source:    s3://$SOURCE_BUCKET/"
echo "    Table:     $TABLE_NAME"
