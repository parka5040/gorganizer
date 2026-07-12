package steam

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseVDF parses Valve's VDF text format into a map structure.
func ParseVDF(r io.Reader) (map[string]interface{}, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	p := &vdfParser{input: string(data)}
	return p.parse()
}

type vdfTokenType int

const (
	vdfString vdfTokenType = iota
	vdfBraceOpen
	vdfBraceClose
	vdfEOF
)

type vdfToken struct {
	typ vdfTokenType
	val string
}

type vdfParser struct {
	input string
	pos   int
}

func (p *vdfParser) skipWhitespaceAndComments() {
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			p.pos++
			continue
		}
		if p.pos+1 < len(p.input) && c == '/' && p.input[p.pos+1] == '/' {
			for p.pos < len(p.input) && p.input[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		break
	}
}

func (p *vdfParser) next() vdfToken {
	p.skipWhitespaceAndComments()

	if p.pos >= len(p.input) {
		return vdfToken{typ: vdfEOF}
	}

	c := p.input[p.pos]

	if c == '{' {
		p.pos++
		return vdfToken{typ: vdfBraceOpen}
	}
	if c == '}' {
		p.pos++
		return vdfToken{typ: vdfBraceClose}
	}
	if c == '"' {
		p.pos++
		var sb strings.Builder
		for p.pos < len(p.input) {
			ch := p.input[p.pos]
			if ch == '\\' && p.pos+1 < len(p.input) {
				escaped := p.input[p.pos+1]
				if escaped == '"' || escaped == '\\' {
					sb.WriteByte(escaped)
					p.pos += 2
					continue
				}
			}
			if ch == '"' {
				p.pos++
				return vdfToken{typ: vdfString, val: sb.String()}
			}
			sb.WriteByte(ch)
			p.pos++
		}
		return vdfToken{typ: vdfString, val: sb.String()}
	}

	var sb strings.Builder
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '{' || ch == '}' {
			break
		}
		sb.WriteByte(ch)
		p.pos++
	}
	return vdfToken{typ: vdfString, val: sb.String()}
}

func (p *vdfParser) parse() (map[string]interface{}, error) {
	rootKey := p.next()
	if rootKey.typ != vdfString {
		return nil, fmt.Errorf("expected root key, got token type %d", rootKey.typ)
	}

	brace := p.next()
	if brace.typ != vdfBraceOpen {
		return nil, fmt.Errorf("expected '{' after root key %q", rootKey.val)
	}

	obj, err := p.parseObject()
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{rootKey.val: obj}, nil
}

func (p *vdfParser) parseObject() (map[string]interface{}, error) {
	result := make(map[string]interface{})
	for {
		key := p.next()
		if key.typ == vdfBraceClose || key.typ == vdfEOF {
			return result, nil
		}
		if key.typ != vdfString {
			return nil, fmt.Errorf("expected string key, got token type %d", key.typ)
		}

		valueOrBrace := p.next()
		switch valueOrBrace.typ {
		case vdfBraceOpen:
			sub, err := p.parseObject()
			if err != nil {
				return nil, err
			}
			result[key.val] = sub
		case vdfString:
			result[key.val] = valueOrBrace.val
		default:
			return nil, fmt.Errorf("expected value or '{' after key %q", key.val)
		}
	}
}

// ParseVDFFromFile opens a file and parses it as VDF.
func ParseVDFFromFile(path string) (map[string]interface{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewReader(f)
	return ParseVDF(sc)
}
