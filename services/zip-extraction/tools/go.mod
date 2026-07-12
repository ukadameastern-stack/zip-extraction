// Tool dependencies. This module's only purpose is to pin CLI tool versions so
// Makefile + CI invoke them deterministically via `go run -modfile=tools/go.mod`.
// See SECURITY-10 / NFR-Z-047 supply-chain pinning policy.

module github.com/org-placeholder/doc-uploader/services/zip-extraction/tools

go 1.25.0

require golang.org/x/vuln v1.6.0

require (
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260708182218-49f421fb7959 // indirect
	golang.org/x/tools v0.48.0 // indirect
)
