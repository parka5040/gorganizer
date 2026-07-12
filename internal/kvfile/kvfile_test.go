package kvfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterScalarBytes(t *testing.T) {
	var w Writer
	w.Comment("Gorganizer test — auto-generated")
	w.KV("raw", "true")
	w.KVQuoted("name", `Say "Hi"`)
	w.KVQuoted("empty", "")
	w.KVQuoted("unicode", "héllo — ★ 日本語")
	w.KVQuoted("hash", "a # b")
	w.KVQuoted("colon", "a: b")
	w.KVBool("flag_true", true)
	w.KVBool("flag_false", false)
	w.KVInt("count", -3)
	w.KVInt64("size", 3861512192)
	want := "# Gorganizer test — auto-generated\n" +
		"raw: true\n" +
		"name: \"Say \\\"Hi\\\"\"\n" +
		"empty: \"\"\n" +
		"unicode: \"héllo — ★ 日本語\"\n" +
		"hash: \"a # b\"\n" +
		"colon: \"a: b\"\n" +
		"flag_true: true\n" +
		"flag_false: false\n" +
		"count: -3\n" +
		"size: 3861512192\n"
	if got := w.String(); got != want {
		t.Errorf("bytes = %q, want %q", got, want)
	}
	if got := string(w.Bytes()); got != want {
		t.Errorf("Bytes() = %q, want %q", got, want)
	}
}

func TestWriterListBytes(t *testing.T) {
	var w Writer
	w.ListHeader("archives")
	w.ItemQuoted("path", `Weird "Quotes"/mod.zip`)
	w.ContInt("mod_id", 266)
	w.ContInt64("bytes", 4096)
	w.ContBool("hidden", true)
	w.ContQuoted("note", "")
	w.ListHeader("files")
	w.ItemString(`docs/read "me".txt`)
	w.ItemString("  lead.txt")
	want := "archives:\n" +
		"  - path: \"Weird \\\"Quotes\\\"/mod.zip\"\n" +
		"    mod_id: 266\n" +
		"    bytes: 4096\n" +
		"    hidden: true\n" +
		"    note: \"\"\n" +
		"files:\n" +
		"  - \"docs/read \\\"me\\\".txt\"\n" +
		"  - \"  lead.txt\"\n"
	if got := w.String(); got != want {
		t.Errorf("bytes = %q, want %q", got, want)
	}
}

func TestWriteAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.yaml")
	var w Writer
	w.Comment("header")
	w.KVBool("ok", true)
	if err := w.WriteAtomic(path, 0644); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "# header\nok: true\n" {
		t.Errorf("bytes = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("perm = %o, want 0644", perm)
	}
}

func TestScannerSkipsBlankAndCommentLines(t *testing.T) {
	in := "# comment\n\n   \n  # indented comment\nkey: value\n"
	sc := NewScanner(strings.NewReader(in))
	var texts []string
	for sc.Scan() {
		texts = append(texts, sc.Line().Text)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("Err: %v", err)
	}
	if len(texts) != 1 || texts[0] != "key: value" {
		t.Errorf("texts = %q, want [\"key: value\"]", texts)
	}
}

func TestScannerLineFields(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Line
	}{
		{
			name: "plain kv",
			in:   "auto_install: true",
			want: Line{Raw: "auto_install: true", Text: "auto_install: true", Item: "auto_install: true"},
		},
		{
			name: "indented list item",
			in:   `  - name: "Core"`,
			want: Line{Raw: `  - name: "Core"`, Text: `- name: "Core"`, IsListItem: true, Item: `name: "Core"`},
		},
		{
			name: "continuation field",
			in:   "    mod_id: 266",
			want: Line{Raw: "    mod_id: 266", Text: "mod_id: 266", Item: "mod_id: 266"},
		},
		{
			name: "bare quoted item",
			in:   `  - "  lead.txt"`,
			want: Line{Raw: `  - "  lead.txt"`, Text: `- "  lead.txt"`, IsListItem: true, Item: `"  lead.txt"`},
		},
		{
			name: "dash without space is not an item",
			in:   "-x: 1",
			want: Line{Raw: "-x: 1", Text: "-x: 1", Item: "-x: 1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := NewScanner(strings.NewReader(tt.in + "\n"))
			if !sc.Scan() {
				t.Fatalf("Scan() = false, err = %v", sc.Err())
			}
			if got := sc.Line(); got != tt.want {
				t.Errorf("Line() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestScannerBufferAllowsLongLines(t *testing.T) {
	long := "key: " + strings.Repeat("a", 100*1024)
	sc := NewScanner(strings.NewReader(long + "\n"))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !sc.Scan() {
		t.Fatalf("Scan() = false, err = %v", sc.Err())
	}
	if sc.Line().Text != long {
		t.Errorf("long line not preserved")
	}
}

func TestCutKV(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		key    string
		value  string
		wantOK bool
	}{
		{name: "plain", in: "key: value", key: "key", value: " value", wantOK: true},
		{name: "no space after colon", in: "key:value", key: "key", value: "value", wantOK: true},
		{name: "padded key", in: "  key : v", key: "key", value: " v", wantOK: true},
		{name: "splits at first colon", in: `path: "colon: name.zip"`, key: "path", value: ` "colon: name.zip"`, wantOK: true},
		{name: "empty value", in: "key:", key: "key", value: "", wantOK: true},
		{name: "no colon", in: "just words", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, v, ok := CutKV(tt.in)
			if ok != tt.wantOK || k != tt.key || v != tt.value {
				t.Errorf("CutKV(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.in, k, v, ok, tt.key, tt.value, tt.wantOK)
			}
		})
	}
}

func TestTrimValue(t *testing.T) {
	if got := TrimValue("   true  "); got != "true" {
		t.Errorf("TrimValue = %q, want %q", got, "true")
	}
	if got := TrimValue(` "quoted" `); got != `"quoted"` {
		t.Errorf("TrimValue = %q, want %q", got, `"quoted"`)
	}
}

func TestUnquoteValueQuirks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain quoted", in: ` "plain.zip"`, want: "plain.zip"},
		{name: "unquoted number", in: " 266", want: "266"},
		{name: "empty quoted", in: ` ""`, want: ""},
		{name: "empty raw", in: "", want: ""},
		{name: "inner escapes stay literal", in: ` "a\"b.zip"`, want: `a\"b.zip`},
		{name: "trailing escaped quote loses quote keeps backslash", in: ` "ends\""`, want: `ends\`},
		{name: "outer spaces inside quotes dropped", in: ` "  spaced.zip  "`, want: "spaced.zip"},
		{name: "colon preserved", in: ` "colon: name.zip"`, want: "colon: name.zip"},
		{name: "hash preserved", in: ` "hash # tag.zip"`, want: "hash # tag.zip"},
		{name: "unicode preserved", in: ` "ünïcode ★.7z"`, want: "ünïcode ★.7z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnquoteValue(tt.in); got != tt.want {
				t.Errorf("UnquoteValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUnquoteItemPreservesInnerSpaces(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: `"textures/sword.dds"`, want: "textures/sword.dds"},
		{name: "leading spaces preserved", in: `"  lead.txt"`, want: "  lead.txt"},
		{name: "trailing spaces preserved", in: `"trail.txt  "`, want: "trail.txt  "},
		{name: "inner escapes stay literal", in: `"a\"b.txt"`, want: `a\"b.txt`},
		{name: "leading hash preserved", in: `"#hash.txt"`, want: "#hash.txt"},
		{name: "unicode preserved", in: `"méshes/★.nif"`, want: "méshes/★.nif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnquoteItem(tt.in); got != tt.want {
				t.Errorf("UnquoteItem(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriteReadRoundTripAsymmetry(t *testing.T) {
	var w Writer
	w.KVQuoted("name", `he"llo`)
	sc := NewScanner(strings.NewReader(w.String()))
	if !sc.Scan() {
		t.Fatalf("Scan() = false, err = %v", sc.Err())
	}
	k, v, ok := CutKV(sc.Line().Item)
	if !ok || k != "name" {
		t.Fatalf("CutKV = (%q, %q, %v)", k, v, ok)
	}
	if got := UnquoteValue(v); got != `he\"llo` {
		t.Errorf("round-trip = %q, want %q", got, `he\"llo`)
	}
}
