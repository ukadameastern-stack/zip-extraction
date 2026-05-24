// Tool dependencies. This module's only purpose is to pin CLI tool versions so
// Makefile + CI invoke them deterministically via `go run -modfile=tools/go.mod`.
// See SECURITY-10 / NFR-Z-047 supply-chain pinning policy.

module github.com/org-placeholder/doc-uploader/services/zip-extraction/tools

go 1.24

require (
	golang.org/x/vuln v1.1.3
)
