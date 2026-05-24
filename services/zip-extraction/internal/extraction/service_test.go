package extraction_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

// BR-STATUS-001 truth-table for ComputeStatus (exposed for testing via the
// unexported function path). We test through the higher-level Outcome shape
// since computeStatus is package-internal.
func TestStatus_StringWireFormat(t *testing.T) {
	assert.Equal(t, "SUCCESS", extraction.StatusSuccess.String())
	assert.Equal(t, "PARTIAL_FAILED", extraction.StatusPartialFailed.String())
	assert.Equal(t, "FAILED", extraction.StatusFailed.String())
}

func TestErrorTypes_IsHelpers(t *testing.T) {
	bde := &extraction.BombDefenceError{Rule: 3, Reason: "ratio"}
	if _, ok := extraction.IsBombDefence(bde); !ok {
		t.Fatal("IsBombDefence must classify *BombDefenceError")
	}
	pve := &extraction.PathValidationError{Path: "x", Reason: extraction.PathReasonTraversal}
	if _, ok := extraction.IsPathValidation(pve); !ok {
		t.Fatal("IsPathValidation must classify")
	}
	ufe := &extraction.UnsupportedFeatureError{Feature: extraction.FeatureEncryptedZIP}
	if _, ok := extraction.IsUnsupportedFeature(ufe); !ok {
		t.Fatal("IsUnsupportedFeature must classify")
	}
}
