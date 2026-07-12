package kvfile

import (
	"bufio"
	"io"
	"strings"
)

type Line struct {
	Raw        string
	Text       string
	IsListItem bool
	Item       string
}

type Scanner struct {
	s    *bufio.Scanner
	line Line
}

// NewScanner returns a Scanner that yields the significant lines of r.
func NewScanner(r io.Reader) *Scanner {
	return &Scanner{s: bufio.NewScanner(r)}
}

// Buffer sets the initial buffer and maximum line size of the underlying scanner.
func (s *Scanner) Buffer(buf []byte, max int) {
	s.s.Buffer(buf, max)
}

// Scan advances past blank and comment lines to the next significant line.
func (s *Scanner) Scan() bool {
	for s.s.Scan() {
		raw := s.s.Text()
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		l := Line{Raw: raw, Text: text, Item: text}
		if strings.HasPrefix(text, "- ") {
			l.IsListItem = true
			l.Item = strings.TrimPrefix(text, "- ")
		}
		s.line = l
		return true
	}
	return false
}

// Line returns the line produced by the last successful Scan.
func (s *Scanner) Line() Line {
	return s.line
}

// Err returns the first error encountered by the underlying scanner.
func (s *Scanner) Err() error {
	return s.s.Err()
}

// CutKV splits s on the first ':', returning the space-trimmed key and the raw value.
func CutKV(s string) (key, value string, ok bool) {
	k, v, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(k), v, true
}

// TrimValue trims surrounding spaces from a raw value without unquoting.
func TrimValue(v string) string {
	return strings.TrimSpace(v)
}

// UnquoteValue trims spaces, strips surrounding double quotes, and trims spaces again, without unescaping.
func UnquoteValue(v string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
}

// UnquoteItem strips surrounding double quotes only, preserving inner spaces, without unescaping.
func UnquoteItem(s string) string {
	return strings.Trim(s, `"`)
}
