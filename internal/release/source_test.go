package release

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLatestTagLimits validates API size, JSON contents and release tag syntax.
func TestLatestTagLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{name: "valid", body: `{"tag_name":"v2.3.4"}`, valid: true},
		{name: "missing", body: `{}`},
		{name: "malformed", body: `{`},
		{name: "oversized", body: strings.Repeat(" ", 1<<20+1)},
		{name: "unsafe tag", body: `{"tag_name":"v1.2.3/../../bad"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) }))
			defer server.Close()
			source := Source{Client: server.Client(), LatestURL: server.URL}
			tag, err := source.ResolveTag(context.Background(), "")
			if tc.valid && (err != nil || tag != "v2.3.4") || !tc.valid && err == nil {
				t.Fatalf("latest tag %q: %v", tag, err)
			}
		})
	}
}
