# Component Dependencies — Zip Extraction Service (UOW-SVC-12)

**Document Type**: Component Dependency Matrix + Data Flow
**Phase**: INCEPTION — Application Design (Part 2: Generation)
**Generated**: 2026-05-24

This document records dependencies between the components defined in `components.md`. Per Q1 (narrow consumer-defined interfaces), domain components depend on **interfaces**, not on adapter implementations directly. Concrete wiring happens once in `cmd/zip-extraction/main.go`.

---

## 1. Dependency Matrix

Rows depend on columns. `■` indicates a direct dependency. `i` indicates an interface dependency (the row component declares an interface that the column component implements). All dependencies flow **downward** (no cycles).

|                       | extraction | bombdefence | validation | storage | dynamodb | slipsheet | retry | sqs | metrics | health | config | awsclients | log |
|-----------------------|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| **cmd/zip-extraction**| ■  | ■  | ■  | ■  | ■  | ■  | ■  | ■  | ■  | ■  | ■  | ■  | ■  |
| **app**               | i  |    |    |    |    |    |    | i  | i  | i  | ■  |    | i  |
| **sqs**               | i (ClaimCheck) |  |  |  |  |  |    |    | i  |    |    |    | i  |
| **extraction**        |    | i  | i  | i  | i  | i  | i  |    | i  |    | ■ (Config types) |    | i  |
| **bombdefence**       |    |    |    |    |    |    |    |    |    |    |    |    |    |
| **validation**        |    |    |    |    |    |    |    |    |    |    |    |    |    |
| **storage**           |    |    |    |    |    |    |    |    |    |    | ■ (Config) |    |    |
| **dynamodb**          |    |    |    |    |    |    |    |    |    |    | ■ (Config) |    |    |
| **slipsheet**         | i (S3Uploader) |  |  |  |  |  |    |    |    |    | ■ (Config) |    |    |
| **retry**             | (uses extraction.Clock/Logger/TransientError types) | | | | | | | | | | ■ (Config) |  |    |
| **metrics**           |    |    |    |    |    |    |    |    |    |    |    |    |    |
| **health**            |    |    |    |    |    |    |    |    |    |    |    |    |    |
| **config**            |    |    |    |    |    |    |    |    |    |    |    |    |    |
| **awsclients**        |    |    |    |    |    |    |    |    |    |    | ■ (InfraConfig) |    |    |
| **log**               |    |    |    |    |    |    |    |    |    |    | ■ (LoggingConfig) |    |    |

### Key observations

1. **`cmd/zip-extraction` is the only universal importer.** It wires all components together. This satisfies SECURITY-11 (separation of concerns — security-critical components don't know about each other's existence).
2. **No cycles.** `extraction` depends on ports it defines itself; adapters in `storage` / `dynamodb` / `slipsheet` etc. depend on extraction's types but not vice-versa.
3. **`bombdefence` and `validation` are pure leaves.** They depend on nothing except stdlib + extraction's error types. This makes them maximally testable (PBT-friendly, no mocks needed) and satisfies SECURITY-11.
4. **`config` is a leaf.** Everything imports `config`'s typed shapes; `config` imports nothing project-internal. This makes startup-time validation a pure operation.
5. **Adapter packages depend only on `extraction`'s port interfaces and `config`.** They do NOT depend on each other — e.g., `storage` does not import `dynamodb`.

---

## 2. Layered Architecture View

```mermaid
flowchart TD
    subgraph CMD["Entry Point"]
        Main["cmd/zip-extraction/main.go"]
    end

    subgraph ORCH["Orchestrator Layer"]
        App["internal/app<br/>Service"]
    end

    subgraph DOM["Domain Layer (pure / no I/O)"]
        Extraction["internal/extraction<br/>(orchestrator + ports + error types)"]
        BombDefence["internal/bombdefence<br/>Checker + LimitedReader"]
        Validation["internal/validation<br/>PathValidator"]
        Retry["internal/retry<br/>Retrier + Classify"]
        Slipsheet["internal/slipsheet<br/>Build + Marshal"]
    end

    subgraph ADAPT["Adapter Layer (I/O boundary)"]
        SQS["internal/sqs<br/>Adapter + Heartbeater"]
        Storage["internal/storage<br/>S3 Adapter + MIME"]
        Dynamo["internal/dynamodb<br/>Adapter"]
        Metrics["internal/metrics<br/>Prometheus"]
        Health["internal/health<br/>HTTP Server + Gate"]
    end

    subgraph INFRA["Infra Layer"]
        AwsClients["internal/awsclients<br/>SDK builders"]
        Log["internal/log<br/>zap factory"]
        Config["internal/config<br/>env + YAML loader"]
    end

    Main --> App
    Main --> Extraction
    Main --> SQS
    Main --> Storage
    Main --> Dynamo
    Main --> Slipsheet
    Main --> BombDefence
    Main --> Validation
    Main --> Retry
    Main --> Metrics
    Main --> Health
    Main --> AwsClients
    Main --> Log
    Main --> Config

    App -->|MessageQueue port| SQS
    App -->|Extractor port| Extraction
    App -->|HealthGate port| Health
    App -->|Metrics port| Metrics
    App -->|Logger port| Log

    SQS -.depends on types.-> Extraction
    Slipsheet -.uses S3Uploader port.-> Extraction
    Retry -.uses Clock/Logger/Error types.-> Extraction

    Extraction -->|S3Downloader, S3Uploader ports| Storage
    Extraction -->|Recorder port| Dynamo
    Extraction -->|SlipsheetWriter port| Slipsheet
    Extraction -->|BombChecker port| BombDefence
    Extraction -->|PathValidator port| Validation
    Extraction -->|Retrier port| Retry
    Extraction -->|Metrics port| Metrics
    Extraction -->|Logger port| Log

    SQS --> AwsClients
    Storage --> AwsClients
    Dynamo --> AwsClients

    AwsClients --> Config
    Log --> Config
    Extraction --> Config
    Storage --> Config
    Dynamo --> Config
    Slipsheet --> Config
    Retry --> Config

    style Main fill:#CE93D8,stroke:#6A1B9A,color:#000
    style App fill:#FFA726,stroke:#E65100,color:#000
    style Extraction fill:#FFCC80,stroke:#E65100,color:#000
    style BombDefence fill:#FFCC80,stroke:#E65100,color:#000
    style Validation fill:#FFCC80,stroke:#E65100,color:#000
    style Retry fill:#FFCC80,stroke:#E65100,color:#000
    style Slipsheet fill:#FFCC80,stroke:#E65100,color:#000
    style SQS fill:#81C784,stroke:#1B5E20,color:#000
    style Storage fill:#81C784,stroke:#1B5E20,color:#000
    style Dynamo fill:#81C784,stroke:#1B5E20,color:#000
    style Metrics fill:#81C784,stroke:#1B5E20,color:#000
    style Health fill:#81C784,stroke:#1B5E20,color:#000
    style AwsClients fill:#90CAF9,stroke:#0D47A1,color:#000
    style Log fill:#90CAF9,stroke:#0D47A1,color:#000
    style Config fill:#90CAF9,stroke:#0D47A1,color:#000
```

---

## 3. Communication Patterns

| Pattern | Where used | Notes |
|---|---|---|
| **Synchronous in-process call** | All domain ↔ adapter calls within a worker goroutine | Standard Go function call through interface dispatch |
| **Goroutine + cancellation context** | SQS receiver, worker pool, heartbeats, HTTP server | One `context.Context` tree rooted in `app.Service.Run` |
| **Bounded semaphore (channel-based)** | Worker pool size = `cfg.MaxInFlight` | A buffered chan struct{} of capacity N — `receiver` writes a token before dispatch, worker reads on completion |
| **HTTP (in-pod only)** | Kubelet → `/healthz/{live,ready}`, Prometheus → `/metrics` | Localhost or ClusterIP; not internet-exposed |
| **AWS SDK (HTTPS)** | All AWS calls | TLS enforced by default; LocalStack endpoint override does NOT disable TLS verification in dev (`AWS_ENDPOINT_URL=http://localstack:4566` is the only legitimate non-HTTPS scheme and it is local-only) |

---

## 4. Data-Flow Diagram (Per-Message Happy Path)

```mermaid
sequenceDiagram
    autonumber
    participant SQS as Amazon SQS
    participant Recv as sqs.receiver
    participant Pool as Worker
    participant Ext as extraction.Service
    participant Val as validation.PathValidator
    participant Bomb as bombdefence.Checker
    participant S3D as storage.Download
    participant S3U as storage.Upload
    participant DDB as dynamodb.Adapter
    participant Slip as slipsheet.Writer
    participant Met as metrics.Metrics

    SQS-->>Recv: ReceiveMessage batch
    Recv->>Pool: dispatch(msg)
    Pool->>Ext: Process(ctx, ClaimCheck)
    Ext->>S3D: Download(sourceBucket, sourceKey)
    S3D-->>Ext: io.ReadCloser, size
    Ext->>Bomb: PreCheck(meta)
    Bomb-->>Ext: nil OR *BombDefenceError
    loop per zip entry
        Ext->>Val: Sanitize(entry.Name)
        Val-->>Ext: safeName OR *PathValidationError
        Ext->>Bomb: EntryCheck(idx, EntryInfo)
        Bomb-->>Ext: nil OR *BombDefenceError
        Ext->>Bomb: NewLimitedReader(rc, ratio)
        Bomb-->>Ext: io.Reader (cap/ratio short-circuit)
        Ext->>S3U: Upload(stagingBucket, "input/{eid}/{idx}-{safe}", limited, size)
        S3U-->>Ext: nil OR *TransientError OR *PermanentError
        Ext->>DDB: RecordEntry(PipelineFile)
        DDB-->>Ext: nil (incl. idempotent conflict)
        Ext->>Met: EntryProcessed("UPLOADED")
        Met-->>Ext: -
    end
    Ext->>Slip: Build + Write({execId, source, status, children})
    Slip->>S3U: Upload(stagingBucket, "slipsheets/{eid}.json", json, len)
    S3U-->>Slip: nil
    Slip-->>Ext: nil
    Ext-->>Pool: Outcome{SUCCESS|PARTIAL_FAILED|FAILED}
    Pool->>SQS: DeleteMessage(receiptHandle)
    Pool->>Met: ExtractionDuration(d, outcome)
```

---

## 5. Boundary Crossing: Local vs Production

All component-to-component edges within the pod use **the same Go function calls** in both environments. The **only edges that change behaviour** between environments are the four outbound AWS-SDK arrows (Download / Upload / RecordEntry / SQS ops):

| Env | Endpoint | TLS? | Auth |
|---|---|---|---|
| Production EKS | `<service>.eu-west-1.amazonaws.com` | TLS 1.2+ | IRSA (no static creds) |
| Local LocalStack | `http://localstack:4566` | None (LocalStack convention) | Dummy `AWS_ACCESS_KEY_ID=test` (LocalStack convention) |

Per the parity analysis in Section 5 of `services.md`, no code path branches on environment. This means the **dependency graph is identical** in both environments — the only difference is the runtime value injected into `awsclients.Build(ctx, cfg.Infra)`.

---

## 6. Compliance Cross-References

| Concern | Resolution |
|---|---|
| **SECURITY-11 separation of concerns** | Two pure security packages (`bombdefence`, `validation`) are leaf nodes; their callers depend on them via Extractor ports — they themselves depend on nothing project-internal |
| **SECURITY-15 fail-closed** | Every adapter returns typed errors (`*TransientError` / `*PermanentError`); `extraction.Service` never silently swallows; `cmd/zip-extraction` has a top-level recover that logs + exits non-zero |
| **PBT-07 generator quality** | Because domain components depend only on small interfaces, PBT generators inject fake adapters with deterministic behaviour — no Testcontainers needed for property tests |
| **PBT-10 complementary tests** | Adapter packages have Gate 2 (Testcontainers/LocalStack) integration tests; domain packages have unit + PBT tests with fakes. Test files are organised so the boundary is obvious |

---

## 7. Hand-off to Functional Design

Functional Design (CONSTRUCTION phase) will refine:

- The **port interface definitions** in `extraction/ports.go` with full Godoc including expected error types and goroutine-safety guarantees.
- The **error-classification table** (which AWS SDK error codes map to which typed error) — needed for `retry.Classify`.
- The **rapid-test generator definitions** for `ClaimCheck`, `PipelineFile`, `Slipsheet`, `ArchiveMetadata`, and `EntryInfo` — used across all PBT properties.
