package release

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"time"
)

var ErrUnreachable = errors.New("release server is unreachable")

type TransportError struct{ Err error }

// Error returns the release transport failure text.
func (e *TransportError) Error() string { return "downloading release: " + e.Err.Error() }

// Unwrap returns the transport failure cause.
func (e *TransportError) Unwrap() error { return e.Err }

// Is classifies connectivity and certificate failures.
func (e *TransportError) Is(target error) bool {
	return target == ErrUnreachable && connectivityError(e.Err)
}

type StatusError struct {
	Code       int
	Status     string
	RetryAfter time.Duration
}

// Error returns the HTTP release failure text.
func (e *StatusError) Error() string { return "release download returned " + e.Status }

type readError struct{ err error }

// Error returns the release body-read failure text.
func (e *readError) Error() string { return "reading release file: " + e.err.Error() }

// Unwrap returns the body-read failure cause.
func (e *readError) Unwrap() error { return e.err }

// Is classifies connectivity failures during a body read.
func (e *readError) Is(target error) bool {
	return target == ErrUnreachable && (connectivityError(e.err) || errors.Is(e.err, io.ErrUnexpectedEOF))
}

// connectivityError reports whether a failure arose from the network or TLS verification.
func connectivityError(err error) bool {
	var dns *net.DNSError
	var op *net.OpError
	var network net.Error
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &dns) || errors.As(err, &op) || errors.As(err, &network) && network.Timeout() || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// retryDelay parses a server retry interval within the one-hour bound.
func retryDelay(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

// statusError constructs an HTTP status failure with its retry interval.
func statusError(code int, status string, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	return &StatusError{Code: code, Status: status, RetryAfter: delay}
}
