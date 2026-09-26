package smapi

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"unicode/utf16"
)

// TestStandardize verifies comments, trailing commas and BOMs are removed while other bytes are preserved.
func TestStandardize(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "strict object unchanged", input: "{\n  \"a\": 1,\n  \"b\": [true, null, \"x\"]\n}", want: "{\n  \"a\": 1,\n  \"b\": [true, null, \"x\"]\n}"},
		{name: "empty input", input: "", want: ""},
		{name: "utf8 bom", input: "\xef\xbb\xbf{\"a\":1}", want: "{\"a\":1}"},
		{name: "line comment end of line", input: "{\n  \"a\": 1, // note\n  \"b\": 2\n}", want: "{\n  \"a\": 1, \n  \"b\": 2\n}"},
		{name: "line comment before document", input: "// header\n{}", want: "\n{}"},
		{name: "line comment at eof", input: "{} // end", want: "{} "},
		{name: "line comment ends at carriage return", input: "{// x\r\"a\":1}", want: "{\r\"a\":1}"},
		{name: "block comment inline", input: "{\"a\": /* x */ 1}", want: "{\"a\":  1}"},
		{name: "block comment before key", input: "{/* x */\"a\":1}", want: "{\"a\":1}"},
		{name: "block comment multiline", input: "{\n/*\n * many\n * lines\n */\n\"a\":1}", want: "{\n\n\"a\":1}"},
		{name: "block comment keeps tokens apart", input: "[1/**/2]", want: "[1 2]"},
		{name: "block comment with stars", input: "{/*** x **/\"a\":1}", want: "{\"a\":1}"},
		{name: "line comment marker inside string", input: "{\"url\":\"https://example.com//x\"}", want: "{\"url\":\"https://example.com//x\"}"},
		{name: "block comment marker inside string", input: "{\"a\":\"/* not a comment */\"}", want: "{\"a\":\"/* not a comment */\"}"},
		{name: "escaped quote inside string", input: "{\"a\":\"say \\\"hi\\\" // still text\"}", want: "{\"a\":\"say \\\"hi\\\" // still text\"}"},
		{name: "escaped backslash ends string", input: "{\"a\":\"c:\\\\\"} // c", want: "{\"a\":\"c:\\\\\"} "},
		{name: "trailing comma object", input: "{\"a\":1,}", want: "{\"a\":1}"},
		{name: "trailing comma array", input: "[1,2,]", want: "[1,2]"},
		{name: "trailing comma with whitespace", input: "[1,2 ,\n\t]", want: "[1,2 \n\t]"},
		{name: "trailing commas nested", input: "{\"a\":{\"b\":[1,[2,],],},}", want: "{\"a\":{\"b\":[1,[2]]}}"},
		{name: "trailing comma after comment", input: "{\"a\":1, // last\n}", want: "{\"a\":1 \n}"},
		{name: "trailing comma before block comment", input: "[1, /* c */ ]", want: "[1  ]"},
		{name: "comma inside string kept", input: "{\"a\":\",}\",\"b\":\",]\"}", want: "{\"a\":\",}\",\"b\":\",]\"}"},
		{name: "separator comma kept", input: "[1, 2]", want: "[1, 2]"},
		{name: "lone slash passes through", input: "{\"a\":1}/", want: "{\"a\":1}/"},
		{name: "utf16le bom", input: "\xff\xfe{\x00\"\x00a\x00\"\x00:\x001\x00}\x00", want: "{\"a\":1}"},
		{name: "utf16be bom", input: "\xfe\xff\x00{\x00\"\x00a\x00\"\x00:\x001\x00}", want: "{\"a\":1}"},
		{name: "utf16 dangling byte", input: "\xff\xfe[\x00]\x00x", want: "[]\ufffd"},
		{name: "utf16 unpaired surrogate", input: "\xff\xfe[\x00\"\x00\x00\xd8\"\x00]\x00", want: "[\"\ufffd\"]"},
		{name: "utf32le bom", input: "\xff\xfe\x00\x00{\x00\x00\x00}\x00\x00\x00", want: "{}"},
		{name: "utf32be bom", input: "\x00\x00\xfe\xff\x00\x00\x00[\x00\x00\x00]", want: "[]"},
		{name: "raw control characters in string", input: "{\"a\":\"x\ty\nz\r\x01\x1f\"}", want: "{\"a\":\"x\\u0009y\\u000az\\u000d\\u0001\\u001f\"}"},
		{name: "nbsp indentation", input: "{\n\u00a0\u00a0\"a\":\u00a01\u2003}", want: "{\n  \"a\": 1 }"},
		{name: "ascii vertical tab and form feed", input: "[1,\v\f2]", want: "[1,  2]"},
		{name: "line and paragraph separators", input: "[1\u2028,\u20292]", want: "[1 , 2]"},
		{name: "unicode whitespace inside strings kept", input: "{\"a\":\"x\u00a0y\u2003\"}", want: "{\"a\":\"x\u00a0y\u2003\"}"},
		{name: "trailing comma before unicode whitespace", input: "[1,\u00a0]", want: "[1 ]"},
		{name: "single-quoted strings", input: "{'a':'x'}", want: "{\"a\":\"x\"}"},
		{name: "single-quoted string with double quotes", input: "['say \"hi\"']", want: "[\"say \\\"hi\\\"\"]"},
		{name: "escaped single quote in single-quoted string", input: "['it\\'s']", want: "[\"it's\"]"},
		{name: "escaped single quote in double-quoted string", input: "[\"it\\'s\"]", want: "[\"it's\"]"},
		{name: "single-quoted string keeps other escapes", input: "['a\\\\b\\n\\u00e9\\\"\\/']", want: "[\"a\\\\b\\n\\u00e9\\\"\\/\"]"},
		{name: "single-quoted string with control character", input: "['a\tb']", want: "[\"a\\u0009b\"]"},
		{name: "comment markers inside single-quoted string", input: "['// x /* y */']", want: "[\"// x /* y */\"]"},
		{name: "unquoted property names", input: "{Name: \"x\", $id_1: 2, \u00dcn\u00ef: 3, 1st: 4}", want: "{\"Name\": \"x\", \"$id_1\": 2, \"\u00dcn\u00ef\": 3, \"1st\": 4}"},
		{name: "unquoted names beside comments", input: "{\n  // c\n  Name: 1,\n  Version /* v */ : \"1.0\",\n}", want: "{\n  \n  \"Name\": 1,\n  \"Version\"  : \"1.0\"\n}"},
		{name: "literal values are never quoted", input: "{a: true, b: [null, false, x], c: {d: -1}, e: 'f'}", want: "{\"a\": true, \"b\": [null, false, x], \"c\": {\"d\": -1}, \"e\": \"f\"}"},
		{name: "unquoted name that looks like a literal", input: "{true: null}", want: "{\"true\": null}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Standardize([]byte(tc.input))
			if err != nil {
				t.Fatalf("Standardize(%q): %v", tc.input, err)
			}
			if string(got) != tc.want {
				t.Fatalf("Standardize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestStandardizeProducesValidJSON verifies a realistic commented manifest decodes after standardizing.
func TestStandardizeProducesValidJSON(t *testing.T) {
	input := "\xef\xbb\xbf{\n" +
		"  // The mod name.\n" +
		"  \"Name\": \"Example // Mod\",\n" +
		"  /* dependencies\n     go here */\n" +
		"  \"Dependencies\": [\n" +
		"    { \"UniqueID\": \"a.b\", }, // first\n" +
		"    { \"UniqueID\": \"c.d\", /* inline */ },\n" +
		"  ],\n" +
		"}\n"
	got, err := Standardize([]byte(input))
	if err != nil {
		t.Fatalf("Standardize: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	want := map[string]any{
		"Name":         "Example // Mod",
		"Dependencies": []any{map[string]any{"UniqueID": "a.b"}, map[string]any{"UniqueID": "c.d"}},
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded = %#v, want %#v", decoded, want)
	}
}

// TestStandardizeLeavesStrictJSONUnchanged verifies strict documents come back byte for byte.
func TestStandardizeLeavesStrictJSONUnchanged(t *testing.T) {
	inputs := []string{
		`{"a":[],"b":{},"c":"\u00e9\\\"/\t","d":-1.5e+10,"e":[true,false,null],"f":"it's"}`,
		"{\"g\":\"x\u00a0y\u2028\"}",
		"[\n\t{ \"Name\" : \"A B\" , \"Deps\" : [ { \"UniqueID\" : \"x.y\" } ] }\r\n]",
		`"just a string"`,
		`12.5`,
		`{"url":"https://example.com/a,b]c}"}`,
	}
	for _, input := range inputs {
		got, err := Standardize([]byte(input))
		if err != nil {
			t.Fatalf("Standardize(%q): %v", input, err)
		}
		if string(got) != input {
			t.Fatalf("Standardize(%q) = %q, want it unchanged", input, got)
		}
	}
}

// TestStandardizeUTF16Manifest verifies a UTF-16 manifest with a byte order mark decodes to the same manifest as UTF-8.
func TestStandardizeUTF16Manifest(t *testing.T) {
	text := "{\r\n  // comment\r\n  'Name': 'Caf\u00e9 \U0001F600',\r\n  UniqueID: 'a.b',\r\n}"
	units := utf16.Encode([]rune(text))
	littleEndian := []byte{0xff, 0xfe}
	bigEndian := []byte{0xfe, 0xff}
	for _, unit := range units {
		littleEndian = binary.LittleEndian.AppendUint16(littleEndian, unit)
		bigEndian = binary.BigEndian.AppendUint16(bigEndian, unit)
	}
	want := map[string]any{"Name": "Caf\u00e9 \U0001F600", "UniqueID": "a.b"}
	for name, input := range map[string][]byte{"little endian": littleEndian, "big endian": bigEndian} {
		got, err := Standardize(input)
		if err != nil {
			t.Fatalf("%s: Standardize: %v", name, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(got, &decoded); err != nil {
			t.Fatalf("%s: json.Unmarshal(%q): %v", name, got, err)
		}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("%s: decoded = %#v, want %#v", name, decoded, want)
		}
	}
}

// TestStandardizeNewtonsoftManifest verifies a manifest using every tolerated extension decodes to the expected values.
func TestStandardizeNewtonsoftManifest(t *testing.T) {
	input := "\xef\xbb\xbf{\n" +
		"\u00a0\u00a0Name: 'Tom\\'s \"Mod\"',\n" +
		"\u00a0\u00a0'Description': \"Line one\n\tLine two\",\n" +
		"\u00a0\u00a0UniqueID: \"tom.mod\", /* id */\n" +
		"\u00a0\u00a0Dependencies: [ { UniqueID: 'a.b', IsRequired: false, }, ],\n" +
		"}"
	got, err := Standardize([]byte(input))
	if err != nil {
		t.Fatalf("Standardize: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", got, err)
	}
	want := map[string]any{
		"Name":         "Tom's \"Mod\"",
		"Description":  "Line one\n\tLine two",
		"UniqueID":     "tom.mod",
		"Dependencies": []any{map[string]any{"UniqueID": "a.b", "IsRequired": false}},
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded = %#v, want %#v", decoded, want)
	}
}

// TestStandardizeErrors verifies unterminated comments and strings are rejected.
func TestStandardizeErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{name: "unterminated block comment", input: "{\"a\":1 /* never closed", want: errUnterminatedComment},
		{name: "block comment closed by its own opener", input: "{/*/", want: errUnterminatedComment},
		{name: "unterminated string", input: "{\"a\":\"open", want: errUnterminatedString},
		{name: "string ending in escaped quote", input: "{\"a\":\"x\\\"}", want: errUnterminatedString},
		{name: "unterminated single-quoted string", input: "{'a", want: errUnterminatedString},
		{name: "single-quoted string ending in backslash", input: "['x\\", want: errUnterminatedString},
		{name: "single-quoted string closed by a double quote", input: "['x\"]", want: errUnterminatedString},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Standardize([]byte(tc.input))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Standardize(%q) error = %v, want %v", tc.input, err, tc.want)
			}
		})
	}
}
