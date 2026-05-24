package extraction_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

func TestDescribeFailure(t *testing.T) {
	cases := []struct {
		name    string
		reason  string
		detail  string
		mustHas []string
	}{
		{"bomb1", "bomb-defence rule 1", "", []string{"rule 1", "compressed archive size"}},
		{"bomb3-with-detail", "bomb-defence rule 3", "compression ratio 100.31x exceeds cap 100.00x",
			[]string{"rule 3", "zip bomb", "100.31x"}},
		{"bomb10", "bomb-defence rule 10", "aborted after 240 seconds",
			[]string{"rule 10", "time limit", "240 seconds"}},
		{"path-traversal", extraction.PathReasonTraversal, "rejected path: \"../etc/passwd\"",
			[]string{"parent-directory traversal", "../etc/passwd"}},
		{"unsupported-encrypted", "unsupported: " + extraction.FeatureEncryptedZIP, "",
			[]string{"encrypted"}},
		{"retries-throttling", "retries exhausted: " + extraction.TransientClassThrottling, "",
			[]string{"rate-limited", "transient"}},
		{"drain", "drain canceled", "", []string{"SIGTERM", "idempotent"}},
		{"corrupt", "corrupt-zip: bad header", "bad header", []string{"central directory", "bad header"}},
		{"unknown", "fictional reason", "extra", []string{"extra"}}, // returns detail when no enum match
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := extraction.DescribeFailure(tc.reason, tc.detail)
			assert.NotEmpty(t, got)
			for _, s := range tc.mustHas {
				assert.Contains(t, got, s, "missing %q in %q", s, got)
			}
		})
	}
}

func TestDescribeFailure_EmptyInputs(t *testing.T) {
	assert.Empty(t, extraction.DescribeFailure("", ""))
}
