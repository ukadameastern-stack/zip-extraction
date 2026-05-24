package extraction_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/org-placeholder/doc-uploader/services/zip-extraction/internal/extraction"
)

func TestErrorMessages(t *testing.T) {
	bde := &extraction.BombDefenceError{Rule: 3, Reason: "ratio"}
	assert.Contains(t, bde.Error(), "rule 3")
	assert.Contains(t, bde.Error(), "ratio")

	pve := &extraction.PathValidationError{Path: "../etc", Reason: extraction.PathReasonTraversal}
	assert.Contains(t, pve.Error(), extraction.PathReasonTraversal)
	assert.Contains(t, pve.Error(), "../etc")

	ufe := &extraction.UnsupportedFeatureError{Feature: extraction.FeatureEncryptedZIP}
	assert.Contains(t, ufe.Error(), extraction.FeatureEncryptedZIP)

	inner := errors.New("boom")
	te := &extraction.TransientError{Cause: inner, Class: extraction.TransientClass5xx}
	assert.Contains(t, te.Error(), extraction.TransientClass5xx)
	assert.Equal(t, inner, te.Unwrap())

	pe := &extraction.PermanentError{Cause: inner}
	assert.Contains(t, pe.Error(), "permanent")
	assert.Equal(t, inner, pe.Unwrap())
}

func TestStatusStringUnknown(t *testing.T) {
	s := extraction.Status(99)
	assert.Equal(t, "UNKNOWN", s.String())
}

func TestIs_NilErrors(t *testing.T) {
	_, ok := extraction.IsBombDefence(nil)
	assert.False(t, ok)
	_, ok = extraction.IsPathValidation(nil)
	assert.False(t, ok)
	_, ok = extraction.IsUnsupportedFeature(nil)
	assert.False(t, ok)
	_, ok = extraction.IsTransient(nil)
	assert.False(t, ok)
	_, ok = extraction.IsPermanent(nil)
	assert.False(t, ok)
}

func TestIs_PlainError(t *testing.T) {
	e := fmt.Errorf("plain")
	_, ok := extraction.IsBombDefence(e)
	assert.False(t, ok)
	_, ok = extraction.IsTransient(e)
	assert.False(t, ok)
}

func TestSystemClock_Now(t *testing.T) {
	c := extraction.SystemClock{}
	t1 := c.Now()
	t2 := c.Now()
	assert.False(t, t2.Before(t1))
}
