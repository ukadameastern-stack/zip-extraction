// Tool dependencies. This module's only purpose is to pin CLI tool versions so
// Makefile + CI invoke them deterministically via `go run -modfile=tools/go.mod`.
// See SECURITY-10 / NFR-Z-047 supply-chain pinning policy.

module github.com/org-placeholder/doc-uploader/services/zip-extraction/tools

go 1.24

require golang.org/x/vuln v1.1.3

require (
	golang.org/x/mod v0.19.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	golang.org/x/telemetry v0.0.0-20240522233618-39ace7a40ae7 // indirect
	golang.org/x/tools v0.23.0 // indirect
)
