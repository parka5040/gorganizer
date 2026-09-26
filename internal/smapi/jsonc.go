package smapi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

var (
	errUnterminatedComment = errors.New("standardizing JSON: unterminated block comment")
	errUnterminatedString  = errors.New("standardizing JSON: unterminated string")
)

const lowerHexDigits = "0123456789abcdef"

type standardizer struct {
	in           []byte
	out          []byte
	containers   []byte
	expectName   bool
	pendingComma int
}

// Standardize converts JSON that Newtonsoft's JsonTextReader tolerates into strict JSON, leaving strict JSON unchanged.
func Standardize(data []byte) ([]byte, error) {
	s := standardizer{in: decodeText(data), pendingComma: -1}
	s.out = make([]byte, 0, len(s.in))
	if err := s.run(); err != nil {
		return nil, err
	}
	return s.out, nil
}

// run rewrites the whole input token by token outside string literals.
func (s *standardizer) run() error {
	i := 0
	for i < len(s.in) {
		ch := s.in[i]
		switch {
		case ch == '/' && i+1 < len(s.in) && s.in[i+1] == '/':
			i = s.skipLineComment(i)
		case ch == '/' && i+1 < len(s.in) && s.in[i+1] == '*':
			next, err := s.skipBlockComment(i)
			if err != nil {
				return err
			}
			i = next
		case isJSONSpace(ch):
			s.out = append(s.out, ch)
			i++
		case ch == '"' || ch == '\'':
			s.beginToken(ch)
			next, err := s.copyString(i)
			if err != nil {
				return err
			}
			s.expectName = false
			i = next
		default:
			r, size := utf8.DecodeRune(s.in[i:])
			switch {
			case unicode.IsSpace(r):
				s.out = append(s.out, ' ')
			case s.expectName && isIdentifierRune(r):
				s.beginToken(ch)
				size = s.quoteName(i) - i
				s.expectName = false
			default:
				s.beginToken(ch)
				s.out = append(s.out, s.in[i:i+size]...)
				s.endToken(ch)
			}
			i += size
		}
	}
	return nil
}

// beginToken drops a pending trailing comma when ch closes a container and forgets it before any other token.
func (s *standardizer) beginToken(ch byte) {
	if s.pendingComma >= 0 && (ch == '}' || ch == ']') {
		s.out = append(s.out[:s.pendingComma], s.out[s.pendingComma+1:]...)
	}
	s.pendingComma = -1
}

// endToken updates the container stack and property-name expectation after a one-byte token was written.
func (s *standardizer) endToken(ch byte) {
	switch ch {
	case '{', '[':
		s.containers = append(s.containers, ch)
		s.expectName = ch == '{'
	case '}', ']':
		if len(s.containers) > 0 {
			s.containers = s.containers[:len(s.containers)-1]
		}
		s.expectName = false
	case ',':
		s.pendingComma = len(s.out) - 1
		s.expectName = len(s.containers) > 0 && s.containers[len(s.containers)-1] == '{'
	default:
		s.expectName = false
	}
}

// skipLineComment skips a line comment up to, but not including, the line break that ends it.
func (s *standardizer) skipLineComment(i int) int {
	i += 2
	for i < len(s.in) && s.in[i] != '\n' && s.in[i] != '\r' {
		i++
	}
	return i
}

// skipBlockComment skips a block comment, keeping adjacent scalar tokens separated by a space.
func (s *standardizer) skipBlockComment(i int) (int, error) {
	end := bytes.Index(s.in[i+2:], []byte("*/"))
	if end < 0 {
		return 0, errUnterminatedComment
	}
	next := i + 2 + end + 2
	if len(s.out) > 0 && next < len(s.in) && isTokenByte(s.out[len(s.out)-1]) && isTokenByte(s.in[next]) {
		s.out = append(s.out, ' ')
	}
	return next, nil
}

// copyString writes a single- or double-quoted string as a strict double-quoted JSON string and returns the index after it.
func (s *standardizer) copyString(i int) (int, error) {
	quote := s.in[i]
	s.out = append(s.out, '"')
	for j := i + 1; j < len(s.in); j++ {
		ch := s.in[j]
		switch {
		case ch == quote:
			s.out = append(s.out, '"')
			return j + 1, nil
		case ch == '\\':
			if j+1 >= len(s.in) {
				return 0, errUnterminatedString
			}
			j++
			switch s.in[j] {
			case '\'':
				s.out = append(s.out, '\'')
			default:
				s.out = append(s.out, '\\', s.in[j])
			}
		case ch == '"':
			s.out = append(s.out, '\\', '"')
		case ch < 0x20:
			s.out = append(s.out, '\\', 'u', '0', '0', lowerHexDigits[ch>>4], lowerHexDigits[ch&0x0f])
		default:
			s.out = append(s.out, ch)
		}
	}
	return 0, errUnterminatedString
}

// quoteName writes an unquoted property name as a JSON string and returns the index after it.
func (s *standardizer) quoteName(i int) int {
	start := i
	for i < len(s.in) {
		r, size := utf8.DecodeRune(s.in[i:])
		if !isIdentifierRune(r) {
			break
		}
		i += size
	}
	s.out = append(s.out, '"')
	s.out = append(s.out, s.in[start:i]...)
	s.out = append(s.out, '"')
	return i
}

// isIdentifierRune reports whether r may appear in a Newtonsoft unquoted property name.
func isIdentifierRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}

// decodeText strips a UTF-8 byte order mark or transcodes UTF-16 and UTF-32 text with one to UTF-8, like File.ReadAllText.
func decodeText(data []byte) []byte {
	switch {
	case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
		return data[3:]
	case bytes.HasPrefix(data, []byte{0xff, 0xfe, 0x00, 0x00}):
		return decodeUTF32(data[4:], binary.LittleEndian)
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0xfe, 0xff}):
		return decodeUTF32(data[4:], binary.BigEndian)
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		return decodeUTF16(data[2:], binary.BigEndian)
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		return decodeUTF16(data[2:], binary.LittleEndian)
	}
	return data
}

// decodeUTF16 transcodes UTF-16 code units to UTF-8, replacing unpaired surrogates and a dangling byte with U+FFFD.
func decodeUTF16(data []byte, order binary.ByteOrder) []byte {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		units = append(units, order.Uint16(data[i:]))
	}
	out := make([]byte, 0, len(data))
	for _, r := range utf16.Decode(units) {
		out = utf8.AppendRune(out, r)
	}
	if len(data)%2 != 0 {
		out = utf8.AppendRune(out, utf8.RuneError)
	}
	return out
}

// decodeUTF32 transcodes UTF-32 code points to UTF-8, replacing invalid code points and trailing bytes with U+FFFD.
func decodeUTF32(data []byte, order binary.ByteOrder) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i+3 < len(data); i += 4 {
		out = utf8.AppendRune(out, rune(order.Uint32(data[i:])))
	}
	if len(data)%4 != 0 {
		out = utf8.AppendRune(out, utf8.RuneError)
	}
	return out
}

// isJSONSpace reports whether ch is insignificant JSON whitespace.
func isJSONSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

// isTokenByte reports whether ch belongs to a scalar token that a removed comment must keep separated.
func isTokenByte(ch byte) bool {
	if isJSONSpace(ch) {
		return false
	}
	switch ch {
	case '{', '}', '[', ']', ',', ':':
		return false
	}
	return true
}
