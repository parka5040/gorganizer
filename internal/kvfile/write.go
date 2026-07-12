package kvfile

import (
	"os"
	"strconv"
	"strings"

	"github.com/parka/gorganizer/internal/atomicfile"
)

type Writer struct {
	b strings.Builder
}

// Comment emits "# <s>\n".
func (w *Writer) Comment(s string) {
	w.b.WriteString("# ")
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

// KV emits "key: rawValue\n" with the value unquoted.
func (w *Writer) KV(key, rawValue string) {
	w.b.WriteString(key)
	w.b.WriteString(": ")
	w.b.WriteString(rawValue)
	w.b.WriteByte('\n')
}

// KVQuoted emits "key: <value>\n" with the value Go %q-quoted.
func (w *Writer) KVQuoted(key, value string) {
	w.KV(key, strconv.Quote(value))
}

// KVBool emits "key: true\n" or "key: false\n".
func (w *Writer) KVBool(key string, v bool) {
	w.KV(key, strconv.FormatBool(v))
}

// KVInt emits "key: <v>\n" in base 10.
func (w *Writer) KVInt(key string, v int) {
	w.KV(key, strconv.Itoa(v))
}

// KVInt64 emits "key: <v>\n" in base 10.
func (w *Writer) KVInt64(key string, v int64) {
	w.KV(key, strconv.FormatInt(v, 10))
}

// ListHeader emits "key:\n".
func (w *Writer) ListHeader(key string) {
	w.b.WriteString(key)
	w.b.WriteString(":\n")
}

// ItemQuoted starts a list item, emitting "  - key: <value>\n" with the value %q-quoted.
func (w *Writer) ItemQuoted(key, value string) {
	w.b.WriteString("  - ")
	w.b.WriteString(key)
	w.b.WriteString(": ")
	w.b.WriteString(strconv.Quote(value))
	w.b.WriteByte('\n')
}

// ItemString emits a bare list item "  - <value>\n" with the value %q-quoted.
func (w *Writer) ItemString(value string) {
	w.b.WriteString("  - ")
	w.b.WriteString(strconv.Quote(value))
	w.b.WriteByte('\n')
}

func (w *Writer) cont(key, rawValue string) {
	w.b.WriteString("    ")
	w.b.WriteString(key)
	w.b.WriteString(": ")
	w.b.WriteString(rawValue)
	w.b.WriteByte('\n')
}

// ContQuoted emits an item continuation field "    key: <value>\n" with the value %q-quoted.
func (w *Writer) ContQuoted(key, value string) {
	w.cont(key, strconv.Quote(value))
}

// ContBool emits an item continuation field "    key: true\n" or "    key: false\n".
func (w *Writer) ContBool(key string, v bool) {
	w.cont(key, strconv.FormatBool(v))
}

// ContInt emits an item continuation field "    key: <v>\n" in base 10.
func (w *Writer) ContInt(key string, v int) {
	w.cont(key, strconv.Itoa(v))
}

// ContInt64 emits an item continuation field "    key: <v>\n" in base 10.
func (w *Writer) ContInt64(key string, v int64) {
	w.cont(key, strconv.FormatInt(v, 10))
}

// Bytes returns the accumulated file contents.
func (w *Writer) Bytes() []byte {
	return []byte(w.b.String())
}

// String returns the accumulated file contents as a string.
func (w *Writer) String() string {
	return w.b.String()
}

// WriteAtomic writes the accumulated contents to path via internal/atomicfile.
func (w *Writer) WriteAtomic(path string, perm os.FileMode) error {
	return atomicfile.WriteFile(path, w.Bytes(), perm)
}
