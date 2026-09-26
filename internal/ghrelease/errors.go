package ghrelease

import (
	"errors"
	"fmt"
)

var (
	ErrNotStable           = errors.New("release is not stable")
	ErrNoDigest            = errors.New("release asset has no published SHA-256 digest")
	ErrNoMatchingAsset     = errors.New("release has no matching asset")
	ErrDigestMismatch      = errors.New("SHA-256 digest mismatch")
	ErrInvalidRelease      = errors.New("invalid release")
	ErrTooLarge            = errors.New("release payload exceeds its size limit")
	ErrInvalidCurrent      = errors.New("invalid current manifest")
	ErrNoPrevious          = errors.New("no previous version")
	ErrPreviousUnavailable = errors.New("previous version is unavailable")
)

type labeledError struct {
	message string
	errs    []error
}

// Error returns the labeled message without the sentinel text.
func (e labeledError) Error() string {
	return e.message
}

// Unwrap exposes both the sentinel and the formatted cause to errors.Is and errors.As.
func (e labeledError) Unwrap() []error {
	return e.errs
}

// tagged formats an error like fmt.Errorf and additionally matches sentinel under errors.Is.
func tagged(sentinel error, format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	return labeledError{message: err.Error(), errs: []error{sentinel, err}}
}

type digestMismatchError struct {
	label    string
	expected string
	actual   string
}

// Error reports the expected and actual digests.
func (e digestMismatchError) Error() string {
	if e.label == "" {
		return "SHA-256 mismatch: expected " + e.expected + ", got " + e.actual
	}
	return e.label + " SHA-256 mismatch: expected " + e.expected + ", got " + e.actual
}

// Unwrap returns ErrDigestMismatch so callers can match it with errors.Is.
func (e digestMismatchError) Unwrap() error {
	return ErrDigestMismatch
}
