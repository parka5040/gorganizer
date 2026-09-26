package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCppViolations locks that only aligned one-line comments directly above declarations outside function bodies pass.
func TestCppViolations(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []cppViolation
	}{
		{
			name: "function definition header",
			src:  "namespace app {\n\n// Does the work.\nvoid Worker::run(int n)\n{\n    step(n);\n}\n\n}\n",
		},
		{
			name: "method declaration header in class body",
			src:  "class Worker : public QObject {\n    Q_OBJECT\npublic:\n    // Does the work.\n    void run(int n);\n    // Compares two workers.\n    bool operator==(const Worker& other) const;\nprivate:\n    struct Job {\n        // Returns the job id.\n        int id() const { return m_id; }\n        int m_id = 0;\n    };\n};\n",
		},
		{
			name: "anonymous namespace helper and inline definition",
			src:  "namespace app {\nnamespace {\n\n// Returns the key.\nstatic QString keyFor(const QString& id) { return \"dl:\" + id; }\n\n}\n}\n",
		},
		{
			name: "indented comment above a call in a function body",
			src:  "Model::Model(QObject* parent)\n    : QObject(parent)\n{\n    // Repaint on theme change.\n    connect(theme, &Theme::changed, this, [this] { update(); });\n}\n",
			want: []cppViolation{{line: 4, kind: "in-body"}},
		},
		{
			name: "unindented comment inside a function body",
			src:  "void run()\n{\n// Starts the step.\n    step();\n}\n",
			want: []cppViolation{{line: 3, kind: "in-body"}},
		},
		{
			name: "comment above a local function inside a lambda body",
			src:  "auto make = [](int n) {\n    // Builds one.\n    int build(int n);\n    return n;\n};\n",
			want: []cppViolation{{line: 2, kind: "in-body"}},
		},
		{
			name: "comment inside a class declared in a function body",
			src:  "void run()\n{\n    struct Local {\n        // Runs locally.\n        void go();\n    };\n}\n",
			want: []cppViolation{{line: 4, kind: "in-body"}},
		},
		{
			name: "comment inside an enum",
			src:  "enum Roles {\n    // First role.\n    First = role(1),\n};\n",
			want: []cppViolation{{line: 2, kind: "in-body"}},
		},
		{
			name: "comment above a macro invocation",
			src:  "// Registers the type.\nQ_DECLARE_METATYPE(app::Worker)\n",
			want: []cppViolation{{line: 1, kind: "non-header"}},
		},
		{
			name: "comment above a directly initialized variable",
			src:  "// The shared timer.\nstatic QTimer timer(nullptr);\n// The pattern.\nstatic const QRegularExpression pattern(QStringLiteral(\"x\"));\n",
			want: []cppViolation{{line: 1, kind: "non-header"}, {line: 3, kind: "non-header"}},
		},
		{
			name: "comment above an assignment-initialized variable",
			src:  "// The limit.\nconst int limit = compute(3);\n",
			want: []cppViolation{{line: 1, kind: "non-header"}},
		},
		{
			name: "comment above a statement keyword line",
			src:  "// Always returns.\nreturn (value);\n",
			want: []cppViolation{{line: 1, kind: "non-header"}},
		},
		{
			name: "indented comment at namespace scope",
			src:  "  // Does the work.\n  void run();\n",
			want: []cppViolation{{line: 1, kind: "indented"}},
		},
		{
			name: "comment not aligned with its declaration",
			src:  "class Worker {\n// Does the work.\n    void run();\n};\n",
			want: []cppViolation{{line: 2, kind: "misaligned"}},
		},
		{
			name: "comment above a type",
			src:  "// A worker.\nclass Worker {\n};\n",
			want: []cppViolation{{line: 1, kind: "non-header"}},
		},
		{
			name: "trailing, multi-line and block comments",
			src:  "void run(); // trailing\n// first\n// second\nvoid stop();\n/* block */\nvoid go();\n",
			want: []cppViolation{{line: 1, kind: "trailing"}, {line: 2, kind: "multi-line"}, {line: 5, kind: "block"}},
		},
		{
			name: "braces and slashes inside literals and directives",
			src:  "#define OPEN {\nvoid run()\n{\n    const char* s = \"{ // not a comment\";\n    char c = '{';\n}\n\n// Stops the work.\nvoid stop();\n",
		},
		{
			name: "constructor initializer braces before the body",
			src:  "Worker::Worker()\n    : m_count{0}\n{\n    // Counts.\n    count();\n}\n\n// Runs.\nvoid Worker::run()\n{\n}\n",
			want: []cppViolation{{line: 4, kind: "in-body"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cppViolations(tc.src)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("cppViolations() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckCppFileCountsViolations locks that the file-level check reports each violation of a file on disk.
func TestCheckCppFileCountsViolations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "widget.cpp")
	src := strings.Join([]string{
		"// Builds the widget.",
		"Widget::Widget()",
		"{",
		"    // Repaint later.",
		"    update();",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := checkCppFile(path); got != 1 {
		t.Fatalf("checkCppFile() = %d, want 1", got)
	}
}
