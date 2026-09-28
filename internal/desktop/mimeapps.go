package desktop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const mimeKey = "x-scheme-handler/nxm="
const maxMimeappsSize = 1 << 20

// EditMimeapps changes only Gorganizer's NXM associations in a UTF-8 MIME settings file.
func EditMimeapps(body []byte, register bool) ([]byte, error) {
	if len(body) > maxMimeappsSize || !utf8.Valid(body) {
		return nil, fmt.Errorf("NXM settings are too large or are not valid text")
	}
	text := string(body)
	if !register && text == "" {
		return body, nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	section := ""
	foundDefault, foundAdded := false, false
	seenDefault, seenAdded := false, false
	addedOthers := []string{}
	if register {
		for _, line := range lines {
			clean := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if strings.HasPrefix(clean, "[") && strings.HasSuffix(clean, "]") {
				section = strings.TrimSuffix(strings.TrimPrefix(clean, "["), "]")
			} else if section == "Added Associations" && strings.HasPrefix(clean, mimeKey) {
				for _, entry := range strings.Split(strings.TrimPrefix(clean, mimeKey), ";") {
					if entry == Handler || entry == "" {
						continue
					}
					duplicate := false
					for _, previous := range addedOthers {
						if previous == entry {
							duplicate = true
							break
						}
					}
					if !duplicate {
						addedOthers = append(addedOthers, entry)
					}
				}
			}
		}
	}
	section = ""
	out := make([]string, 0, len(lines)+6)
	insertMissing := func() {
		if !register {
			return
		}
		if section == "Default Applications" && !seenDefault || section == "Added Associations" && !seenAdded {
			if len(out) > 0 && !strings.HasSuffix(out[len(out)-1], "\n") {
				out = append(out, newline)
			}
			value := Handler
			if section == "Added Associations" {
				if len(addedOthers) > 0 {
					value += ";" + strings.Join(addedOthers, ";")
				}
				seenAdded = true
			} else {
				seenDefault = true
			}
			out = append(out, mimeKey+value+";"+newline)
		}
	}
	for _, line := range lines {
		clean := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.HasPrefix(clean, "[") && strings.HasSuffix(clean, "]") {
			insertMissing()
			section = strings.TrimSuffix(strings.TrimPrefix(clean, "["), "]")
			if section == "Default Applications" {
				foundDefault = true
			}
			if section == "Added Associations" {
				foundAdded = true
			}
			out = append(out, line)
			continue
		}
		if !strings.HasPrefix(clean, mimeKey) || section != "Default Applications" && section != "Added Associations" {
			out = append(out, line)
			continue
		}
		ending := strings.TrimPrefix(line, clean)
		entries := strings.Split(strings.TrimPrefix(clean, mimeKey), ";")
		filtered := make([]string, 0, len(entries)+1)
		wasOurs := false
		for _, entry := range entries {
			if entry == Handler {
				wasOurs = true
				continue
			}
			if entry == "" {
				continue
			}
			filtered = append(filtered, entry)
		}
		if section == "Default Applications" {
			if seenDefault && register {
				continue
			}
			seenDefault = true
			if register {
				out = append(out, mimeKey+Handler+";"+ending)
			} else if wasOurs {
				if len(filtered) > 0 {
					out = append(out, mimeKey+strings.Join(filtered, ";")+";"+ending)
				}
			} else {
				out = append(out, line)
			}
		} else {
			if seenAdded && register {
				continue
			}
			seenAdded = true
			if register {
				out = append(out, mimeKey+Handler+";"+strings.Join(addedOthers, ";"))
				if len(addedOthers) > 0 {
					out[len(out)-1] += ";"
				}
				out[len(out)-1] += ending
			} else if wasOurs {
				if len(filtered) > 0 {
					out = append(out, mimeKey+strings.Join(filtered, ";")+";"+ending)
				}
			} else {
				out = append(out, line)
			}
		}
	}
	insertMissing()
	if register {
		for _, missing := range []struct {
			found bool
			name  string
		}{{foundDefault, "Default Applications"}, {foundAdded, "Added Associations"}} {
			if missing.found {
				continue
			}
			if len(out) != 0 && !strings.HasSuffix(out[len(out)-1], "\n") {
				out = append(out, newline)
			}
			if len(out) != 0 && out[len(out)-1] != newline {
				out = append(out, newline)
			}
			out = append(out, "["+missing.name+"]"+newline, mimeKey+Handler+";"+newline)
		}
	}
	updated := []byte(strings.Join(out, ""))
	if len(updated) > maxMimeappsSize {
		return nil, fmt.Errorf("NXM settings would be too large")
	}
	return updated, nil
}

// UpdateMimeapps reads and atomically updates the user's NXM associations.
func UpdateMimeapps(path string, register bool) error {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking NXM settings: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) && !register {
		return nil
	}
	var body []byte
	perm := os.FileMode(0o644)
	if info != nil {
		if !info.Mode().IsRegular() || info.Size() > maxMimeappsSize {
			return fmt.Errorf("NXM settings are not a regular, reasonably sized file")
		}
		perm = info.Mode().Perm()
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("reading NXM settings: %w", err)
		}
		body, err = io.ReadAll(io.LimitReader(file, maxMimeappsSize+1))
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("reading NXM settings: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("closing NXM settings: %w", closeErr)
		}
	}
	updated, err := EditMimeapps(body, register)
	if err != nil {
		return err
	}
	if string(updated) == string(body) && info != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating NXM settings folder: %w", err)
	}
	if err := atomicfile.WriteFile(path, updated, perm); err != nil {
		return fmt.Errorf("updating NXM settings: %w", err)
	}
	return nil
}

// HasAssociation reports whether Gorganizer owns the default and first added NXM association.
func HasAssociation(body []byte) bool {
	if len(body) > maxMimeappsSize || !utf8.Valid(body) {
		return false
	}
	section := ""
	defaultFound, addedFound := false, false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
		} else if strings.HasPrefix(line, mimeKey) {
			value := strings.TrimPrefix(line, mimeKey)
			if section == "Default Applications" {
				defaultFound = value == Handler+";"
			}
			if section == "Added Associations" {
				addedFound = strings.HasPrefix(value, Handler+";")
			}
		}
	}
	return defaultFound && addedFound
}
