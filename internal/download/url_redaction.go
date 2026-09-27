package download

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// redactURL removes credentials and request parameters from a download URL or NXM link.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Opaque != "" {
		return "<redacted>"
	}
	if u.Scheme == "nxm" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 4 || parts[0] != "mods" || parts[2] != "files" {
			return "<redacted>"
		}
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return "<redacted>"
		}
		if _, err := strconv.Atoi(parts[3]); err != nil {
			return "<redacted>"
		}
		u.Host = u.Hostname()
		u.Path = "/mods/" + parts[1] + "/files/" + parts[3]
		u.RawPath = ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

var (
	httpBodyURL    = regexp.MustCompile(`(?i)\b(?:https?|nxm)://[^\s"'<>\\]+`)
	httpBodySecret = regexp.MustCompile(`(?i)\b(key|expires|user_id)\s*=\s*[^&\s"'<>\\]+`)
)

// RedactHTTPBody strips URLs and credential parameters from a short HTTP error body.
func RedactHTTPBody(body string) string {
	body = httpBodyURL.ReplaceAllStringFunc(body, redactURL)
	return httpBodySecret.ReplaceAllStringFunc(body, func(match string) string {
		idx := strings.IndexByte(match, '=')
		return match[:idx+1] + redactedAPIKey
	})
}

// RedactHTTPError removes the request URL's credentials while keeping the transport cause.
func RedactHTTPError(err error) error {
	return redactHTTPError(err)
}

type redactedTransportError struct {
	message string
	cause   error
}

// Error returns the sanitized transport error message.
func (e *redactedTransportError) Error() string { return e.message }

// Unwrap returns the original transport error cause.
func (e *redactedTransportError) Unwrap() error { return e.cause }

// redactHTTPError replaces request URLs and credential parameters while retaining the cause.
func redactHTTPError(err error) error {
	if urlErr, ok := err.(*url.Error); ok {
		clean := *urlErr
		clean.URL = redactURL(urlErr.URL)
		clean.Err = redactHTTPError(urlErr.Err)
		return &clean
	}
	if err == nil {
		return nil
	}
	clean := RedactHTTPBody(err.Error())
	if clean != err.Error() {
		return &redactedTransportError{message: clean, cause: err}
	}
	return err
}
