# Group H Summary — Step 38 (Gate 2 Testcontainers + LocalStack E2E)

`services/zip-extraction/test/e2e/localstack_test.go` (tagged `//go:build e2e`) implements the Gate 2 integration test harness per NFR-Z-082.

Components:
- `setupLocalStack(t)` spins up a LocalStack container (image `localstack/localstack:3.7`) via `testcontainers-go/modules/localstack`, then provisions: S3 staging bucket, SQS main queue + DLQ + redrive policy (`maxReceiveCount=3`, `VisibilityTimeout=300`), and DynamoDB `pipeline_files` table with PK+SK schema and on-demand billing.
- `makeArchive(t, entries)` builds an in-memory ZIP for tests to upload.
- `uploadArchive(t, env, key, body)` puts the ZIP into the staging bucket via the in-pod AWS clients pointed at LocalStack.
- `TestE2E_HappyPath_SUCCESS` is a smoke test verifying provisioning + DDB reachability.

Placeholders documented for the Build & Test stage to fill in:
- `TestE2E_BombDefence_RejectsRule1`
- `TestE2E_PathTraversal_FAILED`
- `TestE2E_TransientRetry_PARTIAL_FAILED`
- `TestE2E_Redelivery_Idempotent`

Invocation: `go test -tags=e2e ./test/e2e/...`. Default `go test ./...` skips e2e tests via the build tag — matches NFR-9.2 (Gate 2 is a separate gate) and avoids slowing local development by spinning Docker containers in every test invocation.

Gate 3 (sandbox EKS E2E) is **deferred** per Q11 of requirements verification. The chart README will document the future hand-off when the platform team provisions a sandbox EKS environment with real IRSA credentials.
