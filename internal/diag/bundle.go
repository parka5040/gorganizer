package diag

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"golang.org/x/sys/unix"
)

const maxLogBytes = 2 << 20

var (
	nxmLink          = regexp.MustCompile(`(?i)nxm://[^\s"'<>]+`)
	urlQuery         = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>?]+\?[^\s"'<>]+`)
	secretAssignment = regexp.MustCompile(`(?i)\b[a-z0-9_-]*(?:key|token|secret|password)\b\s*["']?\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s&,"'<>}\]]+)`)
	authorization    = regexp.MustCompile(`(?im)(authorization\s*:\s*)[^\r\n"']+`)
)

type BundleOptions struct {
	OutDir   string
	Version  string
	Checkout string
	Report   Report
	Now      time.Time
}

// Redact removes sensitive URL parameters, authentication values, links, and home paths from text.
func Redact(text, home string) string {
	text = nxmLink.ReplaceAllString(text, "[redacted link]")
	text = urlQuery.ReplaceAllStringFunc(text, func(url string) string {
		return url[:strings.IndexByte(url, '?')+1] + "[redacted query]"
	})
	text = authorization.ReplaceAllString(text, "${1}[redacted]")
	text = secretAssignment.ReplaceAllStringFunc(text, func(value string) string {
		separator := strings.IndexAny(value, "=:")
		prefix := value[:separator+1]
		suffix := value[separator+1:]
		spaces := len(suffix) - len(strings.TrimLeft(suffix, " \t"))
		prefix += suffix[:spaces]
		suffix = suffix[spaces:]
		if strings.HasPrefix(suffix, `"`) || strings.HasPrefix(suffix, "'") {
			quote := suffix[:1]
			return prefix + quote + "[redacted]" + quote
		}
		return prefix + "[redacted]"
	})
	if home != "" && home != "/" {
		text = strings.ReplaceAll(text, home, "~")
	}
	return text
}

// CreateBundle writes a private archive of selected diagnostic text with secrets removed.
func CreateBundle(opts BundleOptions) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home folder: %w", err)
	}
	outDir := opts.OutDir
	if outDir == "" {
		outDir = StateDir()
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			return "", fmt.Errorf("creating report folder: %w", err)
		}
	}
	info, err := os.Stat(outDir)
	if err != nil {
		return "", fmt.Errorf("checking report destination: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("report destination must be a folder")
	}
	name := "gorganizer-report-" + opts.Now.UTC().Format("20060102-150405.000000000") + ".tar.gz"
	path := filepath.Join(outDir, name)
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("a report with this name already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("checking report destination: %w", err)
	}
	var content bytes.Buffer
	archive := gzip.NewWriter(&content)
	tarWriter := tar.NewWriter(archive)
	add := func(name string, body []byte) error {
		body = []byte(Redact(string(body), home))
		if strings.HasPrefix(name, "logs/") && len(body) > maxLogBytes {
			body = body[:maxLogBytes]
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), ModTime: opts.Now}); err != nil {
			return fmt.Errorf("adding %s to report: %w", name, err)
		}
		if _, err := tarWriter.Write(body); err != nil {
			return fmt.Errorf("writing %s to report: %w", name, err)
		}
		return nil
	}
	if err := add("doctor.txt", []byte(opts.Report.Text())); err != nil {
		return "", err
	}
	version := []byte(opts.Version + "\n")
	if opts.Checkout != "" {
		if info, err := os.Lstat(filepath.Join(opts.Checkout, "go.mod")); err == nil && info.Mode().IsRegular() {
			if sourceVersion, err := readRegular(filepath.Join(opts.Checkout, "VERSION"), 256); err == nil {
				version = sourceVersion
			}
		}
	}
	if err := add("VERSION", version); err != nil {
		return "", err
	}
	cfg := config.DefaultConfig()
	configPath := filepath.Join(config.ConfigDir(), "config.json")
	settings, err := readRegular(configPath, 4<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading settings for report: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(settings, cfg); err != nil {
			return "", fmt.Errorf("parsing settings for report: %w", err)
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(settings, &raw); err != nil {
			return "", fmt.Errorf("parsing settings for report: %w", err)
		}
		redacted, err := json.MarshalIndent(redactSettings(raw), "", "  ")
		if err != nil {
			return "", fmt.Errorf("preparing settings for report: %w", err)
		}
		if err := add("config.json", redacted); err != nil {
			return "", err
		}
	}
	if err := add("games-and-profiles.txt", []byte(gameNames(cfg))); err != nil {
		return "", err
	}
	for _, name := range []string{"daemon.log", "daemon.log.1", "daemon.log.2"} {
		content, err := readLog(filepath.Join(StateDir(), name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("reading selected log %s: %w", name, err)
		}
		if err := add("logs/"+name, content); err != nil {
			return "", err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return "", fmt.Errorf("finishing report: %w", err)
	}
	if err := archive.Close(); err != nil {
		return "", fmt.Errorf("compressing report: %w", err)
	}
	if err := atomicfile.WriteFile(path, content.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("writing private report: %w", err)
	}
	return path, nil
}

// redactSettings removes credential and environment fields at every level of the settings tree.
func redactSettings(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if strings.EqualFold(key, "nexus_api_key") || strings.EqualFold(key, "environment") {
				delete(typed, key)
			} else {
				typed[key] = redactSettings(child)
			}
		}
	case []interface{}:
		for i, child := range typed {
			typed[i] = redactSettings(child)
		}
	}
	return value
}

// readRegular reads a small regular file without following a final symbolic link.
func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("not a regular file within the size limit")
	}
	file, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds the size limit")
	}
	return data, nil
}

// openRegular opens a file without following a final symbolic link.
func openRegular(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("not a regular file")
	}
	return file, nil
}

// readLog copies the final two megabytes of a regular log without following links.
func readLog(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("log is not a regular file")
	}
	file, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > maxLogBytes {
		if _, err := file.Seek(-maxLogBytes, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLogBytes))
	if err != nil {
		return nil, err
	}
	if info.Size() > maxLogBytes {
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			data = data[end+1:]
		} else {
			data = nil
		}
	}
	return data, nil
}

// gameNames lists configured games and profile directory names without opening profiles.
func gameNames(cfg *config.Config) string {
	ids := make([]string, 0, len(cfg.Games))
	for id := range cfg.Games {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&out, "Game: %s (%s)\n", cfg.Games[id].Name, id)
		if !filepath.IsLocal(id) || filepath.Base(id) != id {
			continue
		}
		profilesDir := config.ProfilesDir(id)
		info, err := os.Lstat(profilesDir)
		if err != nil || !info.IsDir() {
			continue
		}
		profiles, err := os.ReadDir(profilesDir)
		if err != nil {
			continue
		}
		for _, profile := range profiles {
			if profile.IsDir() && !strings.HasPrefix(profile.Name(), ".") {
				fmt.Fprintf(&out, "  Profile: %s\n", profile.Name())
			}
		}
	}
	return out.String()
}
