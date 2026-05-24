# Group B Summary — Steps 9–11 (Config / Log / AWS Clients)

| Step | File | Highlights |
|---|---|---|
| 9 | `services/zip-extraction/internal/log/logger.go` | `Logger` interface; `New(cfg, version)` constructor producing JSON (prod) or console (local) per Q10 of requirements; sensitive-field deny-list filter per BR-LOG-002 (`IsSensitiveKey` exported for PBT-03); `Sync()` swallows benign EINVAL on terminal stdout. |
| 10 | `services/zip-extraction/internal/config/config.go` | `Config` aggregate + 7 nested structs; `Load()` reads env + parses YAML at `$CONFIG_PATH` with `KnownFields(true)` strict-decode (FR-14.4); `Validate()` performs range / consistency / required-field checks per NFR-Z-050 / SECURITY-15 fail-closed. |
| 11 | `services/zip-extraction/internal/awsclients/awsclients.go` | `Set` struct of singleton clients (SQS / S3 / DDB / `s3manager.Uploader`); `Build(ctx, InfraConfig)` honours `AWS_ENDPOINT_URL` for LocalStack per FR-15.1; enables `aws.RetryModeAdaptive` so application-level retry layers cleanly. |

Compliance:
- SECURITY-01: TLS via SDK defaults; no `WithDisableSSL`.
- SECURITY-03: zap structured fields + deny-list redaction.
- SECURITY-15: fail-fast `Validate()`.
- NFR-Z-014: bomb thresholds locked via `BombDefenceConfig` typed values.
