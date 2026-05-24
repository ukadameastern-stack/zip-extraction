//go:build tools

// Package tools tracks development-only tool dependencies for the zip-extraction
// service. This file is never compiled into the application; it only forces
// `go mod tidy` to keep tool versions pinned in tools/go.mod.
//
// Invoke pinned tools via:
//   go run -modfile=tools/go.mod golang.org/x/vuln/cmd/govulncheck ./...
//
// SECURITY-10 / NFR-Z-047 — supply-chain pinning policy.
package tools

import (
	_ "golang.org/x/vuln/cmd/govulncheck"
)
