package validation_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/validation"
	"github.com/org-placeholder/doc-uploader/services/zip-extraction/test/generators"
)

func TestSanitize_Examples(t *testing.T) {
	v := validation.New()

	cases := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{"plain", "report.pdf", "report.pdf", ""},
		{"nested", "docs/report.pdf", "report.pdf", ""},
		{"deep_nested", "a/b/c/report.pdf", "report.pdf", ""},
		{"traversal_simple", "../etc/passwd", "", extraction.PathReasonTraversal},
		{"traversal_encoded", "%2e%2e/etc", "", extraction.PathReasonTraversal},
		{"traversal_back", "..\\etc", "", extraction.PathReasonTraversal},
		{"absolute_unix", "/etc/passwd", "", extraction.PathReasonAbsolute},
		{"absolute_win", "C:\\Windows\\System32", "", extraction.PathReasonAbsolute},
		{"empty", "", "", extraction.PathReasonEmpty},
		{"control_char", "report\x00.pdf", "", extraction.PathReasonInvalidName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := v.Sanitize(tc.input)
			if tc.wantErr != "" {
				require.Error(t, err)
				pve, ok := extraction.IsPathValidation(err)
				require.True(t, ok)
				assert.Equal(t, tc.wantErr, pve.Reason)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// PBT-04 idempotence: Sanitize(Sanitize(x)) == Sanitize(x).
func TestPropertySanitizeIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := validation.New()
		raw := generators.RawPath().Draw(t, "raw")
		first, err := v.Sanitize(raw)
		if err != nil {
			return // accept-or-reject; we only care about the accept case
		}
		second, err := v.Sanitize(first)
		require.NoError(t, err)
		require.Equal(t, first, second)
	})
}

// PBT-03 invariant: every accepted output has no '/', '\\', '..', leading '.', and len ≤ 255.
func TestPropertySanitizeInvariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := validation.New()
		raw := generators.RawPath().Draw(t, "raw")
		out, err := v.Sanitize(raw)
		if err != nil {
			return
		}
		require.False(t, strings.ContainsAny(out, "/\\"))
		require.False(t, strings.Contains(out, ".."))
		require.LessOrEqual(t, len(out), 255)
		require.Greater(t, len(out), 0)
	})
}

// PBT-03 negative: traversal-shaped inputs MUST reject.
func TestPropertyTraversalRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := validation.New()
		raw := generators.RawPathTraversal().Draw(t, "rawTraversal")
		_, err := v.Sanitize(raw)
		require.Error(t, err, "input=%q", raw)
		_, ok := extraction.IsPathValidation(err)
		require.True(t, ok)
	})
}

// PBT-03 negative: absolute / drive-letter inputs MUST reject.
func TestPropertyAbsoluteRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		v := validation.New()
		raw := generators.RawPathAbsolute().Draw(t, "rawAbsolute")
		_, err := v.Sanitize(raw)
		require.Error(t, err, "input=%q", raw)
		_, ok := extraction.IsPathValidation(err)
		require.True(t, ok)
	})
}
