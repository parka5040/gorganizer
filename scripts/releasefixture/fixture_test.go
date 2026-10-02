package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/release"
)

// fixtureBundle creates a signed test release with version-printing stand-ins.
func fixtureBundle(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gorganizerctl", "gorganizerd", "gorganizer-gui"} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '"+name+" 0.0.9+fixture\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(root, "bundles", "0.0.9")
	if err := makeBundle("0.0.9", bin, filepath.Join(bin, "gorganizer-gui"), out); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "bundles"), out
}

// TestSignedFixtureInstall checks the full archive, TLS, signature and publication path.
func TestSignedFixtureInstall(t *testing.T) {
	bundles, out := fixtureBundle(t)
	state := t.TempDir()
	handler, err := newFixtureServer(bundles, state, "normal")
	if err != nil {
		t.Fatal(err)
	}
	certificate, ca, err := tlsFixture()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("CA certificate is invalid")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	trustPEM, err := os.ReadFile(filepath.Join(repoRoot(), "internal/release/testdata/signing/test-k1.pub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := release.ParseTrust(trustPEM)
	if err != nil {
		t.Fatal(err)
	}
	manager := &release.Manager{Root: filepath.Join(t.TempDir(), "releases"), Trust: &trust, Source: release.Source{Client: client, BaseURL: server.URL + "/download/", LatestURL: server.URL + "/latest"}}
	version, err := manager.Install(context.Background(), "")
	if err != nil || version != "0.0.9" {
		t.Fatalf("install %q: %v", version, err)
	}
	status, err := manager.Status()
	if err != nil || status.Current != "0.0.9" {
		t.Fatalf("current release: %+v: %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(out, "gorganizer-0.0.9-linux-x86_64.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

// TestSignedFixtureRejectsBadOrMissingSignatures checks rejection before archive download.
func TestSignedFixtureRejectsBadOrMissingSignatures(t *testing.T) {
	bundles, _ := fixtureBundle(t)
	trustPEM, err := os.ReadFile(filepath.Join(repoRoot(), "internal/release/testdata/signing/test-k1.pub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := release.ParseTrust(trustPEM)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"badsig", "nosig"} {
		t.Run(mode, func(t *testing.T) {
			state := t.TempDir()
			handler, err := newFixtureServer(bundles, state, mode)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			manager := &release.Manager{Root: filepath.Join(t.TempDir(), "releases"), Trust: &trust, Source: release.Source{Client: server.Client(), BaseURL: server.URL + "/download/", LatestURL: server.URL + "/latest"}}
			if _, err := manager.Install(context.Background(), ""); err == nil {
				t.Fatal("accepted missing or invalid release signature")
			}
			requests, err := os.ReadFile(filepath.Join(state, "requests.log"))
			if err != nil {
				t.Fatal(err)
			}
			status := "200"
			if mode == "nosig" {
				status = "404"
			}
			if strings.Contains(string(requests), ".tar.gz") || !strings.Contains(string(requests), "GET /download/v0.0.9/SHA256SUMS.sig "+status+"\n") {
				t.Fatalf("unexpected requests: %s", requests)
			}
		})
	}
}
