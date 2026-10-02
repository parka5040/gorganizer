package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/release"
)

type fixtureServer struct {
	versions map[string]string
	latest   string
	mode     string
	logPath  string
	mu       sync.Mutex
}

// newFixtureServer finds each complete fixture archive and its signed metadata.
func newFixtureServer(dir, stateDir, mode string) (*fixtureServer, error) {
	switch mode {
	case "normal", "throttle", "status403", "status429", "nosig", "badsig":
	default:
		return nil, fmt.Errorf("unknown fixture mode %q", mode)
	}
	server := &fixtureServer{versions: make(map[string]string), mode: mode, logPath: filepath.Join(stateDir, "requests.log")}
	roots, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	parents := []string{dir}
	for _, root := range roots {
		if root.IsDir() {
			parents = append(parents, filepath.Join(dir, root.Name()))
		}
	}
	for _, parent := range parents {
		children, err := os.ReadDir(parent)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if !child.IsDir() || !strings.HasPrefix(child.Name(), "gorganizer-") {
				continue
			}
			version := strings.TrimPrefix(child.Name(), "gorganizer-")
			if release.ValidateTag("v"+version) != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(parent, "gorganizer-"+version+"-linux-x86_64.tar.gz")); err != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(parent, "SHA256SUMS")); err != nil {
				continue
			}
			server.versions["v"+version] = parent
			if server.latest == "" {
				server.latest = "v" + version
			} else if cmp, _ := release.Compare(version, strings.TrimPrefix(server.latest, "v")); cmp > 0 {
				server.latest = "v" + version
			}
		}
	}
	if server.latest == "" {
		return nil, fmt.Errorf("no fixture bundles found in %s", dir)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(server.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := log.Close(); err != nil {
		return nil, err
	}
	return server, nil
}

// ServeHTTP serves the latest tag, signed assets and release notes while recording requests.
func (s *fixtureServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		fd, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(fd, "%s %s %d\n", r.Method, r.URL.Path, status)
			fd.Close()
		}
	}()
	if r.Method != http.MethodGet {
		status = http.StatusMethodNotAllowed
		w.WriteHeader(status)
		return
	}
	if r.URL.Path == "/latest" {
		if s.mode == "status403" || s.mode == "status429" {
			if s.mode == "status403" {
				status = http.StatusForbidden
			} else {
				status = http.StatusTooManyRequests
			}
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Tag string `json:"tag_name"`
		}{s.latest})
		return
	}
	if tag, ok := strings.CutPrefix(r.URL.Path, "/notes/"); ok && release.ValidateTag(tag) == nil {
		fmt.Fprintf(w, "Fixture release notes for %s\n", tag)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/download/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/download/") || len(parts) != 2 || release.ValidateTag(parts[0]) != nil {
		status = http.StatusNotFound
		w.WriteHeader(status)
		return
	}
	dir, ok := s.versions[parts[0]]
	asset := "gorganizer-" + strings.TrimPrefix(parts[0], "v") + "-linux-x86_64.tar.gz"
	if !ok || parts[1] != "SHA256SUMS" && parts[1] != "SHA256SUMS.sig" && parts[1] != asset || parts[1] == "SHA256SUMS.sig" && s.mode == "nosig" {
		status = http.StatusNotFound
		w.WriteHeader(status)
		return
	}
	if parts[1] == "SHA256SUMS.sig" && s.mode == "badsig" {
		sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
		if err != nil {
			status = http.StatusInternalServerError
			w.WriteHeader(status)
			return
		}
		w.Write(signFixtureSums("v9.9.9", sums))
		return
	}
	file, err := os.Open(filepath.Join(dir, parts[1]))
	if err != nil {
		status = http.StatusNotFound
		w.WriteHeader(status)
		return
	}
	defer file.Close()
	if parts[1] == asset && s.mode == "throttle" {
		block := make([]byte, 1024)
		for {
			n, err := file.Read(block)
			if n > 0 {
				if _, writeErr := w.Write(block[:n]); writeErr != nil {
					return
				}
				if flush, ok := w.(http.Flusher); ok {
					flush.Flush()
				}
				time.Sleep(time.Second / 16)
			}
			if err != nil {
				return
			}
		}
	}
	io.Copy(w, file)
}

// tlsFixture creates an ephemeral CA and loopback server certificate.
func tlsFixture() (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Gorganizer fixture CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	server := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	private, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))
	return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), err
}

// serve starts the loopback HTTPS server and shuts it down on SIGINT or SIGTERM.
func serve(dir, address, caOut, stateDir, mode string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("fixture listener must bind to 127.0.0.1")
	}
	handler, err := newFixtureServer(dir, stateDir, mode)
	if err != nil {
		return err
	}
	cert, ca, err := tlsFixture()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.WriteFile(caOut, ca, 0o600); err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err := os.WriteFile(filepath.Join(stateDir, "listen.addr"), []byte(listener.Addr().String()+"\n"), 0o600); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
