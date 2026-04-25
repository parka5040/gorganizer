package ini

import (
	"strings"
)

// Document is a line-preserving representation of an INI file. It keeps the
// original byte ordering, comments, blank lines, and whitespace so that
// successive read/edit/write round-trips don't churn the file. Section
// headers are recognized as `[name]` (optionally with leading whitespace),
// keys as `key=value` (first `=` splits). Anything that doesn't parse as a
// key is a raw line (comment, blank, malformed).
type Document struct {
	lines []iniLine
}

type iniLine struct {
	kind    lineKind
	section string // normalized section name (lowercased for matching)
	rawSec  string // raw section header text (preserved)
	key     string // raw key including case
	value   string
	raw     string // used when kind = lineRaw or lineSection (header)
	// indent: leading whitespace before key or header
	indent string
}

type lineKind int

const (
	lineRaw lineKind = iota // comments, blanks, malformed
	lineSection
	lineKey
)

// ParseDocument parses INI text into a round-trippable Document.
func ParseDocument(content string) *Document {
	d := &Document{}
	currentSection := ""
	for _, raw := range splitLines(content) {
		trimmed := strings.TrimSpace(raw)
		indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]

		// Section header.
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			sec := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			currentSection = sec
			d.lines = append(d.lines, iniLine{
				kind:    lineSection,
				section: strings.ToLower(sec),
				rawSec:  sec,
				raw:     raw,
				indent:  indent,
			})
			continue
		}

		// Comment / blank / malformed line.
		if trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
			d.lines = append(d.lines, iniLine{kind: lineRaw, raw: raw})
			continue
		}

		// key=value.
		eq := strings.Index(trimmed, "=")
		if eq < 0 {
			d.lines = append(d.lines, iniLine{kind: lineRaw, raw: raw})
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		val := trimmed[eq+1:]
		d.lines = append(d.lines, iniLine{
			kind:    lineKey,
			section: strings.ToLower(currentSection),
			key:     key,
			value:   val,
			indent:  indent,
		})
	}
	return d
}

// Serialize returns the INI as text, preserving original line contents
// wherever possible and emitting freshly-written keys in the standard
// `key=value` form.
func (d *Document) Serialize() string {
	var b strings.Builder
	for _, ln := range d.lines {
		switch ln.kind {
		case lineRaw:
			b.WriteString(ln.raw)
		case lineSection:
			if ln.raw != "" {
				b.WriteString(ln.raw)
			} else {
				b.WriteString("[")
				b.WriteString(ln.rawSec)
				b.WriteString("]")
			}
		case lineKey:
			b.WriteString(ln.indent)
			b.WriteString(ln.key)
			b.WriteString("=")
			b.WriteString(ln.value)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Get returns the value for section/key (first match) and whether it
// existed. Section matching is case-insensitive; key matching is exact.
func (d *Document) Get(section, key string) (string, bool) {
	sec := strings.ToLower(section)
	for _, ln := range d.lines {
		if ln.kind == lineKey && ln.section == sec && ln.key == key {
			return ln.value, true
		}
	}
	return "", false
}

// Set inserts or overwrites the given key inside the given section. Creates
// the section at the end of the document if missing. If the key already
// exists in the section, its value is replaced in place (preserving line
// position); otherwise it's appended at the end of the section's block.
func (d *Document) Set(section, key, value string) {
	sec := strings.ToLower(section)
	// Find existing key.
	for i, ln := range d.lines {
		if ln.kind == lineKey && ln.section == sec && ln.key == key {
			d.lines[i].value = value
			return
		}
	}
	// Find end of section to append there.
	sectionStart := -1
	for i, ln := range d.lines {
		if ln.kind == lineSection && ln.section == sec {
			sectionStart = i
			break
		}
	}
	newLine := iniLine{kind: lineKey, section: sec, key: key, value: value}
	if sectionStart < 0 {
		// Append new section + key at the end (after a blank for tidiness).
		if len(d.lines) > 0 && d.lines[len(d.lines)-1].kind != lineRaw {
			d.lines = append(d.lines, iniLine{kind: lineRaw, raw: ""})
		}
		d.lines = append(d.lines,
			iniLine{kind: lineSection, section: sec, rawSec: section},
			newLine,
		)
		return
	}
	// Insert at end of this section (just before next section header or EOF).
	insertAt := len(d.lines)
	for i := sectionStart + 1; i < len(d.lines); i++ {
		if d.lines[i].kind == lineSection {
			insertAt = i
			// Walk back past trailing blanks so the new key sits above them.
			for insertAt > sectionStart+1 && d.lines[insertAt-1].kind == lineRaw &&
				strings.TrimSpace(d.lines[insertAt-1].raw) == "" {
				insertAt--
			}
			break
		}
	}
	d.lines = append(d.lines[:insertAt],
		append([]iniLine{newLine}, d.lines[insertAt:]...)...)
}

// Remove drops the given key from the given section. No-op if absent.
func (d *Document) Remove(section, key string) {
	sec := strings.ToLower(section)
	for i, ln := range d.lines {
		if ln.kind == lineKey && ln.section == sec && ln.key == key {
			d.lines = append(d.lines[:i], d.lines[i+1:]...)
			return
		}
	}
}

// Merge overlays every (section, key, value) from `overlay` onto this
// document, replacing existing values and creating sections/keys when they
// don't exist. Used at push time to fold {Game}Custom.ini into the primary
// INI for engines that don't read Custom.ini natively.
func (d *Document) Merge(overlay *Document) {
	for _, ln := range overlay.lines {
		if ln.kind != lineKey {
			continue
		}
		// Reconstruct the original-case section name by scanning overlay.
		section := ln.section
		for _, h := range overlay.lines {
			if h.kind == lineSection && h.section == ln.section {
				section = h.rawSec
				break
			}
		}
		d.Set(section, ln.key, ln.value)
	}
}

// splitLines mirrors the input's line boundaries without eating the final
// line when the content doesn't end in a newline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		// trailing newline created an empty tail — drop it so Serialize can
		// add it back without doubling up.
		out = out[:len(out)-1]
	}
	return out
}
