//go:build !releasefixture

package release

import (
	_ "embed"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const repositoryURL = "https://github.com/parka5040/gorganizer/releases/download/"
const notesURLBase = "https://github.com/parka5040/gorganizer/releases/tag/"
const latestURL = "https://api.github.com/repos/parka5040/gorganizer/releases/latest"

//go:embed release-signing.pub.pem
var productionKeys []byte

// configError reports whether the release configuration is usable.
func configError() error { return nil }

// checkedClient retains production clients' existing redirect behavior.
func checkedClient(client *http.Client) *http.Client { return client }

// checkSourceOrigin leaves explicit source clients to their existing policy.
func checkSourceOrigin(string, bool) error { return nil }

// latestOriginDescription describes the production latest-release origin.
func latestOriginDescription() string { return "api.github.com" }

// assetOriginDescription describes the production asset origins.
func assetOriginDescription() string { return "github.com *.githubusercontent.com" }

// allowLatestOrigin permits only the pinned GitHub API origin.
func allowLatestOrigin(u *url.URL) error { return githubOrigin(u, u.Hostname() == "api.github.com") }

// allowAssetOrigin permits GitHub releases and GitHubusercontent asset hosts.
func allowAssetOrigin(u *url.URL) error {
	return githubOrigin(u, u.Hostname() == "github.com" || strings.HasSuffix(u.Hostname(), ".githubusercontent.com"))
}

// githubOrigin checks the common HTTPS GitHub origin constraints.
func githubOrigin(u *url.URL, host bool) error {
	if !host || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return fmt.Errorf("refusing release origin")
	}
	return nil
}
