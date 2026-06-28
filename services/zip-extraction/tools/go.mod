// Tool dependencies. This module's only purpose is to pin CLI tool versions so
// Makefile + CI invoke them deterministically via `go run -modfile=tools/go.mod`.
// See SECURITY-10 / NFR-Z-047 supply-chain pinning policy.

module github.com/org-placeholder/doc-uploader/services/zip-extraction/tools

go 1.25.0

require golang.org/x/vuln v1.5.0

require (
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/telemetry v0.0.0-20260625142307-59b4966ccb57 // indirect
	golang.org/x/tools v0.47.0 // indirect
)
