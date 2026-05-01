package download

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// LegacyFomodInfo is the parsed form of an NMM-style fomod/info.xml.
type LegacyFomodInfo struct {
	Name           string
	Description    string
	Version        string
	Author         string
	ScreenshotPath string
}

// ParseLegacyFomodInfo reads {moduleRoot}/fomod/info.xml; handles UTF-8 and UTF-16 LE/BE.
func ParseLegacyFomodInfo(moduleRoot string) LegacyFomodInfo {
	info := LegacyFomodInfo{Name: filepath.Base(moduleRoot)}

	fomodDir, err := findCaseInsensitiveChild(moduleRoot, "fomod")
	if err != nil || fomodDir == "" {
		return info
	}
	infoPath, err := findCaseInsensitiveChild(fomodDir, "info.xml")
	if err != nil || infoPath == "" {
		return info
	}

	raw, err := os.ReadFile(infoPath)
	if err != nil {
		return info
	}
	utf8Bytes, err := decodeXMLBytes(raw)
	if err != nil {
		return info
	}

	var doc struct {
		XMLName     xml.Name `xml:"fomod"`
		Name        string   `xml:"Name"`
		Author      string   `xml:"Author"`
		Version     string   `xml:"Version"`
		Description string   `xml:"Description"`
	}
	if err := xml.Unmarshal(utf8Bytes, &doc); err != nil {
		return info
	}
	if name := strings.TrimSpace(doc.Name); name != "" {
		info.Name = name
	}
	info.Author = strings.TrimSpace(doc.Author)
	info.Version = strings.TrimSpace(doc.Version)
	info.Description = strings.TrimSpace(doc.Description)

	if entries, err := os.ReadDir(fomodDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			lower := strings.ToLower(e.Name())
			if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
				if strings.Contains(lower, "screenshot") {
					info.ScreenshotPath = filepath.Join(fomodDir, e.Name())
					break
				}
				if info.ScreenshotPath == "" {
					info.ScreenshotPath = filepath.Join(fomodDir, e.Name())
				}
			}
		}
	}
	return info
}

// decodeXMLBytes converts raw bytes into declaration-free UTF-8 XML.
func decodeXMLBytes(raw []byte) ([]byte, error) {
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		return utf16ToUTF8(raw[2:], binary.LittleEndian), nil
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return utf16ToUTF8(raw[2:], binary.BigEndian), nil
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		raw = raw[3:]
	}
	if i := bytes.Index(raw, []byte("?>")); i > 0 && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("<?xml")) {
		raw = bytes.TrimSpace(raw[i+2:])
	}
	return raw, nil
}

func utf16ToUTF8(b []byte, order binary.ByteOrder) []byte {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = order.Uint16(b[i*2 : i*2+2])
	}
	runes := utf16.Decode(u16)
	out := []byte(string(runes))
	trimmed := bytes.TrimSpace(out)
	if bytes.HasPrefix(trimmed, []byte("<?xml")) {
		if end := bytes.Index(trimmed, []byte("?>")); end > 0 {
			out = bytes.TrimSpace(trimmed[end+2:])
		}
	}
	return out
}
