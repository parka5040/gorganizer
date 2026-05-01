package ini

import (
	"strings"
)

// Document is a line-preserving INI representation that round-trips comments,
// blank lines, and whitespace intact.
type Document struct {
	lines []iniLine
}

type iniLine struct {
	kind    lineKind
	section string
	rawSec  string
	key     string
	value   string
	raw     string
	indent  string
}

type lineKind int

const (
	lineRaw lineKind = iota
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

		if trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
			d.lines = append(d.lines, iniLine{kind: lineRaw, raw: raw})
			continue
		}

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

// Serialize returns the INI as text, preserving original lines where possible.
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

// Get returns the value for section/key; section match is case-insensitive.
func (d *Document) Get(section, key string) (string, bool) {
	sec := strings.ToLower(section)
	for _, ln := range d.lines {
		if ln.kind == lineKey && ln.section == sec && ln.key == key {
			return ln.value, true
		}
	}
	return "", false
}

// Set inserts or overwrites a key inside a section, creating the section at EOF if missing.
func (d *Document) Set(section, key, value string) {
	sec := strings.ToLower(section)
	for i, ln := range d.lines {
		if ln.kind == lineKey && ln.section == sec && ln.key == key {
			d.lines[i].value = value
			return
		}
	}
	sectionStart := -1
	for i, ln := range d.lines {
		if ln.kind == lineSection && ln.section == sec {
			sectionStart = i
			break
		}
	}
	newLine := iniLine{kind: lineKey, section: sec, key: key, value: value}
	if sectionStart < 0 {
		if len(d.lines) > 0 && d.lines[len(d.lines)-1].kind != lineRaw {
			d.lines = append(d.lines, iniLine{kind: lineRaw, raw: ""})
		}
		d.lines = append(d.lines,
			iniLine{kind: lineSection, section: sec, rawSec: section},
			newLine,
		)
		return
	}
	insertAt := len(d.lines)
	for i := sectionStart + 1; i < len(d.lines); i++ {
		if d.lines[i].kind == lineSection {
			insertAt = i
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

// Remove drops a key from a section; no-op if absent.
func (d *Document) Remove(section, key string) {
	sec := strings.ToLower(section)
	for i, ln := range d.lines {
		if ln.kind == lineKey && ln.section == sec && ln.key == key {
			d.lines = append(d.lines[:i], d.lines[i+1:]...)
			return
		}
	}
}

// Merge overlays every (section, key, value) from overlay onto this document.
func (d *Document) Merge(overlay *Document) {
	for _, ln := range overlay.lines {
		if ln.kind != lineKey {
			continue
		}
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

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := strings.Split(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
