package extraction

import (
	"errors"
	"fmt"
)

// BombDefenceError signals a violation of one of the 10 bomb-defence rules.
// Rule is the FR-7 rule number (1..10); Reason is human-readable.
// Per BR-BOMB-007 / BR-RETRY-009 these are NEVER retried.
type BombDefenceError struct {
	Rule   int
	Reason string
}

func (e *BombDefenceError) Error() string {
	return fmt.Sprintf("bomb-defence rule %d: %s", e.Rule, e.Reason)
}

// PathValidationError signals that an entry path failed FR-6 sanitisation.
// Reason ∈ {"path-traversal","absolute-path","empty-name","invalid-filename"}.
type PathValidationError struct {
	Path   string
	Reason string
}

func (e *PathValidationError) Error() string {
	return fmt.Sprintf("path validation: %s (path=%q)", e.Reason, e.Path)
}

// UnsupportedFeatureError signals a ZIP feature this service does not handle
// per FR-3.6. Feature ∈ {"encrypted-zip","multi-disk","deflate64"}.
type UnsupportedFeatureError struct {
	Feature string
}

func (e *UnsupportedFeatureError) Error() string {
	return fmt.Sprintf("unsupported zip feature: %s", e.Feature)
}

// TransientError wraps an underlying error classified as retryable per
// BR-RETRY-014. Class is the classifier bucket — "throttling" | "5xx" |
// "timeout" | "network".
type TransientError struct {
	Cause error
	Class string
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("transient (%s): %v", e.Class, e.Cause)
}

// Unwrap returns the underlying cause so errors.Is/As can traverse the chain.
func (e *TransientError) Unwrap() error { return e.Cause }

// PermanentError wraps an error classified as non-retryable per BR-RETRY-008.
type PermanentError struct {
	Cause error
}

func (e *PermanentError) Error() string {
	return fmt.Sprintf("permanent: %v", e.Cause)
}

// Unwrap returns the underlying cause.
func (e *PermanentError) Unwrap() error { return e.Cause }

// IsBombDefence returns the *BombDefenceError if err (or any error in its
// chain) is one, plus a boolean.
func IsBombDefence(err error) (*BombDefenceError, bool) {
	var e *BombDefenceError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsPathValidation returns the *PathValidationError if err is one.
func IsPathValidation(err error) (*PathValidationError, bool) {
	var e *PathValidationError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsUnsupportedFeature returns the *UnsupportedFeatureError if err is one.
func IsUnsupportedFeature(err error) (*UnsupportedFeatureError, bool) {
	var e *UnsupportedFeatureError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsTransient returns the *TransientError if err is one.
func IsTransient(err error) (*TransientError, bool) {
	var e *TransientError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsPermanent returns the *PermanentError if err is one.
func IsPermanent(err error) (*PermanentError, bool) {
	var e *PermanentError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// PathValidationReason vocabulary (BR-PATH).
const (
	PathReasonTraversal   = "path-traversal"
	PathReasonAbsolute    = "absolute-path"
	PathReasonEmpty       = "empty-name"
	PathReasonInvalidName = "invalid-filename"
)

// TransientError class vocabulary (BR-RETRY-014).
const (
	TransientClassThrottling = "throttling"
	TransientClass5xx        = "5xx"
	TransientClassTimeout    = "timeout"
	TransientClassNetwork    = "network"
)
