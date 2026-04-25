package mod

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ModListEntry represents a single entry in modlist.txt.
type ModListEntry struct {
	Name    string
	Enabled bool
}

// ParseModList reads a modlist.txt from an io.Reader.
// Lines starting with + are enabled, - are disabled.
// Lines starting with # or empty lines are skipped.
// Returns entries in file order (first = lowest priority).
func ParseModList(r io.Reader) ([]ModListEntry, error) {
	var entries []ModListEntry
	scanner := bufio.NewScanner(r)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if len(line) < 2 {
			return nil, fmt.Errorf("modlist line %d: line too short: %q", lineNum, line)
		}

		prefix := line[0]
		name := line[1:]

		switch prefix {
		case '+':
			entries = append(entries, ModListEntry{Name: name, Enabled: true})
		case '-':
			entries = append(entries, ModListEntry{Name: name, Enabled: false})
		default:
			return nil, fmt.Errorf("modlist line %d: expected '+' or '-' prefix, got %q", lineNum, string(prefix))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading modlist: %w", err)
	}
	return entries, nil
}

// WriteModList writes entries to an io.Writer in modlist.txt format.
func WriteModList(w io.Writer, entries []ModListEntry) error {
	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString("# Gorganizer modlist — do not edit while daemon is running\n"); err != nil {
		return err
	}

	for _, e := range entries {
		prefix := byte('+')
		if !e.Enabled {
			prefix = '-'
		}
		if _, err := fmt.Fprintf(bw, "%c%s\n", prefix, e.Name); err != nil {
			return err
		}
	}
	return bw.Flush()
}
