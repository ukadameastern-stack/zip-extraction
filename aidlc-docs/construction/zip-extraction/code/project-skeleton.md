# Project Skeleton Summary — Group A (Steps 1–8)

**Generated**: 2026-05-24
**Workflow stage**: CONSTRUCTION — Code Generation Part 2 (Group A)

| File | Purpose | Source NFR / Q&A |
|---|---|---|
| `services/zip-extraction/go.mod` | Go module declaration with locked dependencies | NFR-Z-047, tech-stack §1 |
| `services/zip-extraction/.gitignore` | VCS-ignored artefacts | — |
| `services/zip-extraction/.dockerignore` | docker-build context exclusions | — |
| `services/zip-extraction/Dockerfile` | Multi-stage multi-arch container build | NFR-Z-046, Q3 of infra plan, tech-stack §14 |
| `services/zip-extraction/Makefile` | Developer + CI command surface | NFR-Z-072, NFR-Z-090 (parity), tech-stack §10 |
| `services/zip-extraction/.golangci.yml` | Linter configuration | NFR-Z-072 |
| `services/zip-extraction/tools/go.mod` + `tools.go` | Pinned development tools (govulncheck) | SECURITY-10 / NFR-Z-047 / NFR-Z-070 |
| `.github/dependabot.yml` (workspace root) | Weekly dependency-update PR bot | NFR-Z-047 |
| `services/zip-extraction/renovate.json` | Renovate alternative config | NFR-Z-047 |

**Key implementation notes**:
- Go 1.24 declared in `go.mod` (Q3 of requirements).
- Dockerfile base images include `# TODO(security): replace with digest-pinned reference` comments. Operators MUST pin before production merge — CI validates pinning during release.
- Makefile target `vuln` invokes govulncheck via the pinned tools module (`go run -modfile=tools/go.mod ...`) — no separate install step needed in CI.
- `.golangci.yml` enables the linter rule set documented in tech-stack §10.
- `dependabot.yml` groups AWS SDK modules into a single PR to reduce reviewer churn.
