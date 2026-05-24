# Performance Test Instructions — zip-extraction (UOW-SVC-12)

**Test Gate**: Optional gate (SLO validation per NFR-Z-010..014)
**Scope**: Load and stress testing against the deployed service (sandbox EKS or LocalStack)

## Performance Targets (from NFR-Z-010..014)

| SLI | Target | Window |
|---|---|---|
| Success-rate | ≥ 99.5% (excluding bomb-defence + unsupported) | 28-day rolling |
| P95 latency | ≤ 180 s for archives ≤ 100 MB / ≤ 100 entries | 28-day rolling |
| P99 latency | ≤ 220 s | 28-day rolling |
| Sustained upload throughput | ≥ 5 MB/s per entry | per upload |
| Per-pod throughput | ≥ 50 archives/min (mixed workload) | sustained |
| Aggregate cluster throughput | ≥ 500 archives/min at max HPA scale (10 pods) | burst |
| Per-pod memory | ≤ 128 MiB under `maxInFlight=5` | sustained |

## Test Environment

Performance tests SHOULD be run against:

1. **LocalStack baseline** — measures application overhead. Useful for regression detection but does NOT validate production SLOs (LocalStack is single-process; AWS production scales independently).
2. **Sandbox EKS** — true production-shape environment. **DEFERRED** per Q11 of requirements verification until the platform team provisions the sandbox.

This document defines the load-generation strategy that will apply to BOTH environments when ready.

## Load Generator

Recommended tool: a small Go program publishing SQS messages at a controlled rate. (No `k6` / `jmeter` because the workload is not HTTP.)

Sketch:

```go
package main

import (
    "context"
    "encoding/json"
    "flag"
    "math/rand"
    "time"

    "github.com/aws/aws-sdk-go-v2/aws"
    awsconfig "github.com/aws/aws-sdk-go-v2/config"
    awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

func main() {
    queueURL := flag.String("queue", "", "SQS queue URL")
    rate := flag.Int("rate", 50, "messages per minute")
    duration := flag.Duration("duration", 5*time.Minute, "total runtime")
    flag.Parse()

    cfg, _ := awsconfig.LoadDefaultConfig(context.Background())
    cli := awssqs.NewFromConfig(cfg)
    interval := time.Minute / time.Duration(*rate)
    deadline := time.Now().Add(*duration)
    for time.Now().Before(deadline) {
        body, _ := json.Marshal(map[string]any{
            "pipelineExecutionId": "exec-perf-" + randID(),
            "tenantId":            "tenant-1",
            "documentId":          "doc-" + randID(),
            "sourceBucket":        "perf-source",
            "sourceKey":           "uploads/" + randSize() + ".zip",
            "correlationId":       "corr-" + randID(),
        })
        _, _ = cli.SendMessage(context.Background(), &awssqs.SendMessageInput{
            QueueUrl: aws.String(*queueURL), MessageBody: aws.String(string(body)),
        })
        time.Sleep(interval)
    }
}

func randID() string { return time.Now().Format("20060102150405") + "-" + randSuffix() }
func randSuffix() string {
    const c = "abcdefghijklmnopqrstuvwxyz0123456789"
    b := make([]byte, 8)
    for i := range b { b[i] = c[rand.Intn(len(c))] }
    return string(b)
}
func randSize() string {
    sizes := []string{"small", "medium", "large"}
    return sizes[rand.Intn(len(sizes))]
}
```

Pre-stage S3 archives at the corresponding `sourceKey`s (one each of small/medium/large bucket sizes per the table below).

## Workload Profiles

| Profile | Archive size mix | Use case |
|---|---|---|
| **Light** | 100% small (1 MB, 10 entries) | Smoke-level SLO validation; ~50 msgs/min |
| **Standard** | 60% small + 30% medium (50 MB, 50 entries) + 10% large (200 MB, 100 entries) | Realistic production-shape mix |
| **Stress** | 100% medium | Saturates `maxInFlight` to verify backpressure |
| **Burst** | Standard mix at 200 msgs/min for 5 min | HPA scaling validation |

## Execution

### 1. Pre-stage S3 archives

```bash
aws s3 cp test/perf-fixtures/small.zip s3://perf-source/uploads/small.zip
aws s3 cp test/perf-fixtures/medium.zip s3://perf-source/uploads/medium.zip
aws s3 cp test/perf-fixtures/large.zip s3://perf-source/uploads/large.zip
```

### 2. Run load generator

```bash
go run ./test/perf/loadgen \
    --queue "$QUEUE_URL" \
    --rate 50 --duration 30m
```

### 3. Observe SLI dashboards during the run

Open Grafana dashboard with the queries from `nfr-requirements.md` §11:

- Success rate (denominator excludes bomb-defence + unsupported)
- P95 / P99 latency
- Bomb-rejection rate
- Redelivery rate
- Slipsheet write failure rate

Plus operational dashboards:
- Pod memory (target ≤ 128 MiB)
- Pod CPU (target steady; spikes during multipart upload acceptable)
- SQS `ApproximateNumberOfMessagesVisible` (HPA scaling driver)
- HPA replica count over time

### 4. Validate against targets

After the run completes:

| Metric | Pass criterion |
|---|---|
| Success rate (window) | ≥ 99.5% (excluding bomb-defence + unsupported FAILEDs) |
| P95 latency | ≤ 180 s |
| P99 latency | ≤ 220 s |
| Per-pod memory peak | ≤ 128 MiB |
| HPA scaled correctly | Replicas matched queue depth / 5 within 2 min |
| DLQ depth | 0 (any redrive indicates an unhandled failure) |

## Performance Optimisation Loop

If targets aren't met:

1. **Latency high** — profile via SIGUSR1 heap dump + a separate goroutine profile (add code-coverage one-shot). Common causes: insufficient `maxInFlight`, S3 multipart parallelism too low, AWS regional latency from a non-eu-west-1 test client.
2. **Memory high** — check `LimitedReader` cap; verify `MaxInMemoryBufferBytes` (4 MiB default); review for accidental `io.ReadAll` introductions.
3. **Throughput low** — inspect SQS visibility / heartbeat behaviour; verify HPA scaling is firing (KEDA scaler logs).
4. **DLQ depth > 0** — investigate failure reasons via `zip_extraction_failures_total` label values.

## Reporting

After each run, generate a summary into `aidlc-docs/construction/build-and-test/performance-runs/run-<RFC3339>.md` containing:

```markdown
# Perf Run <timestamp>

- Workload profile: ...
- Duration: ...
- Image digest: ...

## Results

| Metric | Target | Actual | Pass? |
|---|---|---|---|
| Success rate | 99.5% | ... | ✓/✗ |
| P95 latency | 180s | ... | ✓/✗ |
| P99 latency | 220s | ... | ✓/✗ |
| Per-pod memory peak | 128 MiB | ... | ✓/✗ |

## Findings & Follow-ups
- ...
```

## Status

Performance tests are an **optional gate** for the initial release. Run when:
- Major code-path changes touching streaming I/O
- Major dependency bumps (AWS SDK major version, zap major version)
- HPA / resource-request tuning proposals
- Pre-production rollout into a new environment

Gate 3 sandbox-EKS performance runs are **deferred** until the sandbox is provisioned.
