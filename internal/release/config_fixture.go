//go:build releasefixture

package release

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

var activeFixture = loadFixture()
var latestURL = activeFixture.latest
var repositoryURL = activeFixture.assets
var notesURLBase = activeFixture.notes
var productionKeys = activeFixture.trust

type fixtureConfig struct {
	latest string
	assets string
	notes  string
	origin string
	trust  []byte
	err    error
}

// loadFixture reads and validates the fixture endpoints and trust bundle once.
func loadFixture() fixtureConfig {
	c := fixtureConfig{
		latest: os.Getenv("GORGANIZER_FIXTURE_LATEST_URL"),
		assets: os.Getenv("GORGANIZER_FIXTURE_ASSETS_URL"),
		notes:  os.Getenv("GORGANIZER_FIXTURE_NOTES_URL"),
		origin: os.Getenv("GORGANIZER_FIXTURE_ORIGIN"),
	}
	host, port, err := net.SplitHostPort(c.origin)
	if err != nil || host == "" || strings.ContainsAny(host, " /\t\r\n") || net.JoinHostPort(host, port) != c.origin {
		c.err = fmt.Errorf("invalid GORGANIZER_FIXTURE_ORIGIN: expected one host:port")
		return c
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		c.err = fmt.Errorf("invalid GORGANIZER_FIXTURE_ORIGIN: expected a valid port")
		return c
	}
	for _, endpoint := range []struct{ name, value string }{
		{"GORGANIZER_FIXTURE_LATEST_URL", c.latest},
		{"GORGANIZER_FIXTURE_ASSETS_URL", c.assets},
		{"GORGANIZER_FIXTURE_NOTES_URL", c.notes},
	} {
		u, err := url.Parse(endpoint.value)
		if err != nil || u.Scheme != "https" || u.Host != c.origin || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Path == "" {
			c.err = fmt.Errorf("invalid %s: expected an HTTPS URL on %s", endpoint.name, c.origin)
			return c
		}
	}
	if !strings.HasSuffix(c.assets, "/") || !strings.HasSuffix(c.notes, "/") {
		c.err = fmt.Errorf("invalid fixture URLs: assets and notes URLs must end in /")
		return c
	}
	trustPath := os.Getenv("GORGANIZER_FIXTURE_TRUST_PEM")
	if trustPath == "" {
		c.err = fmt.Errorf("missing GORGANIZER_FIXTURE_TRUST_PEM")
		return c
	}
	c.trust, err = os.ReadFile(trustPath)
	if err != nil {
		c.err = fmt.Errorf("reading GORGANIZER_FIXTURE_TRUST_PEM: %w", err)
		return c
	}
	if _, err := ParseTrust(c.trust); err != nil {
		c.err = fmt.Errorf("invalid GORGANIZER_FIXTURE_TRUST_PEM: %w", err)
	}
	return c
}

// configError reports invalid fixture configuration before any network operation.
func configError() error { return activeFixture.err }

// checkedClient restricts redirects from explicitly supplied fixture clients.
func checkedClient(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many release redirects")
		}
		return fixtureOrigin(req.URL)
	}
	return &copy
}

// checkSourceOrigin checks even requests sent through an explicitly supplied client.
func checkSourceOrigin(address string, latest bool) error {
	u, err := url.Parse(address)
	if err != nil {
		return fmt.Errorf("invalid release source: %w", err)
	}
	if latest {
		return allowLatestOrigin(u)
	}
	return allowAssetOrigin(u)
}

// latestOriginDescription describes the fixture's sole origin.
func latestOriginDescription() string { return activeFixture.origin }

// assetOriginDescription describes the fixture's sole origin.
func assetOriginDescription() string { return activeFixture.origin }

// allowLatestOrigin permits only the configured HTTPS fixture origin.
func allowLatestOrigin(u *url.URL) error { return fixtureOrigin(u) }

// allowAssetOrigin permits only the configured HTTPS fixture origin.
func allowAssetOrigin(u *url.URL) error { return fixtureOrigin(u) }

// fixtureOrigin checks HTTPS requests against the configured host and port.
func fixtureOrigin(u *url.URL) error {
	if err := configError(); err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host != activeFixture.origin || u.User != nil {
		return fmt.Errorf("refusing release origin")
	}
	return nil
}
