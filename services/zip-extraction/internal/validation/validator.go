// Package validation implements FR-6 entry-path safety: traversal, absolute,
// drive-letter, control-char, and invalid-filename rejection per BR-PATH-001..006.
// Sanitize is pure and idempotent (BR-PATH-005, PBT-04).
package validation

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

// PathValidator implements extraction.PathValidator.
type PathValidator struct{}

// New constructs a PathValidator. The validator is stateless; the constructor
// exists for symmetry with the other adapter packages.
func New() *PathValidator { return &PathValidator{} }

const maxFilenameBytes = 255

// Sanitize cleans rawPath and returns the safe base filename or an
// *extraction.PathValidationError. The result is guaranteed to contain no
// directory separators, no `..` segments, no drive-letter prefix, and no
// control characters; length ≤ 255 bytes.
//
// BR-PATH-001 — reject traversal
// BR-PATH-002 — reject absolute / drive-letter
// BR-PATH-003 — return base filename only
// BR-PATH-004 — character + length constraints
// BR-PATH-005 — idempotent
func (v *PathValidator) Sanitize(rawPath string) (string, error) {
	if rawPath == "" {
		return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonEmpty}
	}

	// 1. URL-decode (defends against encoded `%2e%2e` traversal).
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		decoded = rawPath
	}

	// 2. Normalise backslashes to forward slashes (Windows-style traversal).
	decoded = strings.ReplaceAll(decoded, "\\", "/")

	// 3. Reject drive-letter prefix (Windows absolute path).
	if hasDriveLetterPrefix(decoded) {
		return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonAbsolute}
	}

	// 4. Reject Unix-style absolute paths.
	if strings.HasPrefix(decoded, "/") {
		return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonAbsolute}
	}

	// 5. Reject any segment that is exactly ".." after split.
	for _, seg := range strings.Split(decoded, "/") {
		if seg == ".." {
			return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonTraversal}
		}
	}

	// 6. Clean. filepath.Clean turns "foo/./bar" → "foo/bar", "foo/../bar" → "bar"
	//    which would defeat our traversal check, so we already rejected ".." above.
	cleaned := filepath.Clean(decoded)
	cleaned = strings.ReplaceAll(cleaned, "\\", "/") // re-normalise (Clean on Windows uses \)

	// 7. Re-check traversal after Clean.
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonTraversal}
	}

	// 8. Take final segment only — directories are NOT preserved in S3 keys
	//    (the S3 key embeds {execId}/{idx}-{safeName}; entry directory structure
	//    is intentionally flattened).
	idx := strings.LastIndex(cleaned, "/")
	base := cleaned
	if idx >= 0 {
		base = cleaned[idx+1:]
	}

	// 9. Final empty / dot-only check.
	if base == "" || base == "." {
		return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonEmpty}
	}

	// 10. Disallow control characters and non-UTF8 strings.
	if !utf8.ValidString(base) {
		return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonInvalidName}
	}
	for _, r := range base {
		if r < 0x20 || r == 0x7f {
			return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonInvalidName}
		}
	}

	// 11. Length bound.
	if len(base) > maxFilenameBytes {
		return "", &extraction.PathValidationError{Path: rawPath, Reason: extraction.PathReasonInvalidName}
	}

	return base, nil
}

// hasDriveLetterPrefix reports whether s starts with `[A-Za-z]:` (Windows
// absolute path indicator).
func hasDriveLetterPrefix(s string) bool {
	if len(s) < 2 {
		return false
	}
	c := s[0]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
		return false
	}
	return s[1] == ':'
}
