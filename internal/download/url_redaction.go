package download

import (
	"errors"
	"net/url"
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

// redactHTTPError replaces the request URL in a transport error while keeping its cause.
func redactHTTPError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		clean := *urlErr
		clean.URL = redactURL(urlErr.URL)
		return &clean
	}
	return err
}
