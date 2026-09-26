package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var goDirective = regexp.MustCompile(`^//(go:[a-z0-9]+|line |export )`)

var generatedHeader = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.?$`)

var roots = []string{"internal", "cmd", "src", "scripts"}

// main walks the repo's source roots and exits 1 if any comment violates the policy.
func main() {
	violations := 0
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == "build" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			base := d.Name()
			if strings.HasPrefix(base, "moc_") || strings.HasPrefix(base, "qrc_") || strings.HasSuffix(base, ".pb.go") {
				return nil
			}
			switch {
			case strings.HasSuffix(base, ".go"):
				violations += checkGoFile(path)
			case strings.HasSuffix(base, ".cpp") || strings.HasSuffix(base, ".h") || strings.HasSuffix(base, ".hpp"):
				violations += checkCppFile(path)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "commentcheck: %v\n", err)
			os.Exit(2)
		}
	}
	if violations > 0 {
		fmt.Fprintf(os.Stderr, "commentcheck: %d violation(s)\n", violations)
		os.Exit(1)
	}
}

// checkGoFile reports comment-policy violations in one Go file via the AST.
func checkGoFile(path string) int {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: parse error: %v\n", path, err)
		return 1
	}
	for _, cg := range f.Comments {
		if generatedHeader.MatchString(cg.List[0].Text) {
			return 0
		}
	}
	legal := map[*ast.CommentGroup]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Doc != nil && len(fd.Doc.List) == 1 && strings.HasPrefix(fd.Doc.List[0].Text, "//") {
			legal[fd.Doc] = true
		}
		return true
	})
	count := 0
	for _, cg := range f.Comments {
		if legal[cg] {
			continue
		}
		allDirectives := true
		for _, c := range cg.List {
			if !goDirective.MatchString(c.Text) {
				allDirectives = false
				break
			}
		}
		if allDirectives {
			continue
		}
		pos := fset.Position(cg.Pos())
		fmt.Printf("%s:%d: forbidden comment (go)\n", pos.Filename, pos.Line)
		count++
	}
	return count
}

type scopeKind int

const (
	scopeNamespace scopeKind = iota
	scopeClass
	scopeBlock
)

type cppLine struct {
	num          int
	hasCode      bool
	commentStart bool
	blockComment bool
	declScope    bool
	classScope   bool
}

type cppViolation struct {
	line int
	kind string
}

var (
	namespaceBrace = regexp.MustCompile(`(^|[^\w])(namespace(\s+[\w:]+)?|extern\s*"")\s*$`)
	enumBrace      = regexp.MustCompile(`(^|[^\w])enum\b`)
	classBrace     = regexp.MustCompile(`(^|[^\w])(class|struct|union)\b[^=(){};]*$`)
	macroCall      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*\s*\(`)
	leadingWord    = regexp.MustCompile(`^[A-Za-z_]\w*`)
	initializerArg = regexp.MustCompile(`^\(\s*("|'|-?\d|(nullptr|true|false|this|QStringLiteral|QLatin1String|QLatin1Char)\b|u8?"|[uUL]'|[uUL]"|R")`)
)

var statementKeywords = map[string]bool{
	"if": true, "else": true, "for": true, "while": true, "do": true, "switch": true, "case": true,
	"default": true, "return": true, "break": true, "continue": true, "goto": true, "try": true,
	"catch": true, "throw": true, "new": true, "delete": true, "emit": true, "Q_EMIT": true,
	"co_return": true, "co_await": true, "co_yield": true, "sizeof": true, "static_assert": true,
	"using": true, "typedef": true, "namespace": true, "public": true, "protected": true,
	"private": true, "signals": true, "slots": true,
}

// checkCppFile reports comment-policy violations in one C++ file via a small lexer.
func checkCppFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return 1
	}
	found := cppViolations(string(data))
	for _, v := range found {
		fmt.Printf("%s:%d: forbidden comment (%s)\n", path, v.line, v.kind)
	}
	return len(found)
}

// cppViolations returns every comment in src that is not a single aligned line directly above a function declaration outside any function body.
func cppViolations(src string) []cppViolation {
	lines := lexCpp(src)
	raw := strings.Split(src, "\n")
	var found []cppViolation
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		kind := ""
		switch {
		case l.blockComment:
			kind = "block"
		case !l.commentStart:
			continue
		case l.hasCode:
			kind = "trailing"
		case i+1 < len(lines) && lines[i+1].commentStart && !lines[i+1].hasCode:
			kind = "multi-line"
		case !l.declScope:
			kind = "in-body"
		default:
			kind = headerProblem(raw, l)
		}
		if kind != "" {
			found = append(found, cppViolation{line: l.num, kind: kind})
		}
	}
	return found
}

// headerProblem names what disqualifies a comment outside function bodies from being a function header comment, or returns "" when it is one.
func headerProblem(raw []string, l cppLine) string {
	next := ""
	for j := l.num; j < len(raw); j++ {
		if strings.TrimSpace(raw[j]) != "" {
			next = raw[j]
			break
		}
	}
	commentIndent := leadingSpace(raw[l.num-1])
	if commentIndent != leadingSpace(next) {
		return "misaligned"
	}
	if !l.classScope && commentIndent != "" {
		return "indented"
	}
	if !looksLikeDeclaration(next) {
		return "non-header"
	}
	return ""
}

// leadingSpace returns the run of spaces and tabs that starts line.
func leadingSpace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// looksLikeDeclaration reports whether line starts a function or method declaration rather than a statement, macro or variable.
func looksLikeDeclaration(line string) bool {
	t := strings.TrimSpace(line)
	open := strings.Index(t, "(")
	if open <= 0 || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "}") || strings.HasPrefix(t, "{") {
		return false
	}
	if statementKeywords[leadingWord.FindString(t)] || macroCall.MatchString(t) {
		return false
	}
	head := t[:open]
	if !strings.Contains(head, "operator") && (strings.ContainsAny(head, "=.") || strings.Contains(head, "->")) {
		return false
	}
	return !initializerArg.MatchString(t[open:])
}

// classifyBrace decides from the statement text before an opening brace whether it opens a namespace, a class body, or any other block.
func classifyBrace(prefix string) scopeKind {
	switch {
	case namespaceBrace.MatchString(prefix):
		return scopeNamespace
	case enumBrace.MatchString(prefix):
		return scopeBlock
	case classBrace.MatchString(prefix):
		return scopeClass
	}
	return scopeBlock
}

// preprocessorLines marks the lines that belong to preprocessor directives, including backslash continuations.
func preprocessorLines(src string) []bool {
	raw := strings.Split(src, "\n")
	marks := make([]bool, len(raw))
	continued := false
	for i, line := range raw {
		trimmed := strings.TrimSpace(line)
		marks[i] = continued || strings.HasPrefix(trimmed, "#")
		continued = marks[i] && strings.HasSuffix(trimmed, "\\")
	}
	return marks
}

// lexCpp classifies each line, tracking string/char/raw-string/block-comment state and the brace scopes a comment sits in.
func lexCpp(src string) []cppLine {
	var out []cppLine
	preproc := preprocessorLines(src)
	line := cppLine{num: 1}
	inBlock, inStr, inChar, inRaw := false, false, false, false
	rawDelim := ""
	var scopes []scopeKind
	var prefix strings.Builder
	i := 0
	flush := func() {
		out = append(out, line)
		line = cppLine{num: line.num + 1}
	}
	directive := func() bool {
		return line.num-1 < len(preproc) && preproc[line.num-1]
	}
	note := func(text string) {
		if !directive() {
			prefix.WriteString(text)
		}
	}
	for i < len(src) {
		c := src[i]
		if c == '\n' {
			if inBlock {
				line.blockComment = line.blockComment || line.commentStart || true
			}
			note("\n")
			flush()
			i++
			continue
		}
		switch {
		case inRaw:
			if c == ')' && strings.HasPrefix(src[i+1:], rawDelim+`"`) {
				i += 1 + len(rawDelim) + 1
				inRaw = false
				line.hasCode = true
				continue
			}
			line.hasCode = true
			i++
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i += 2
				continue
			}
			i++
		case inStr:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '"' {
				inStr = false
			}
			line.hasCode = true
			i++
		case inChar:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '\'' {
				inChar = false
			}
			line.hasCode = true
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			line.commentStart = true
			line.declScope = true
			for _, kind := range scopes {
				if kind == scopeBlock {
					line.declScope = false
				}
			}
			line.classScope = len(scopes) > 0 && scopes[len(scopes)-1] == scopeClass
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			line.blockComment = true
			inBlock = true
			i += 2
		case c == '"':
			if j := strings.LastIndexAny(src[:i], "\n"); true {
				seg := src[j+1 : i]
				if k := strings.LastIndex(seg, "R"); k >= 0 && k == len(seg)-1 {
					if m := strings.Index(src[i+1:], "("); m >= 0 && m < 20 {
						rawDelim = src[i+1 : i+1+m]
						inRaw = true
						line.hasCode = true
						note(`""`)
						i += 1 + m + 1
						continue
					}
				}
			}
			inStr = true
			line.hasCode = true
			note(`""`)
			i++
		case c == '\'':
			inChar = true
			line.hasCode = true
			note(`''`)
			i++
		default:
			if c != ' ' && c != '\t' && c != '\r' {
				line.hasCode = true
			}
			if directive() {
				i++
				continue
			}
			switch c {
			case '{':
				scopes = append(scopes, classifyBrace(prefix.String()))
				prefix.Reset()
			case '}':
				if len(scopes) > 0 {
					scopes = scopes[:len(scopes)-1]
				}
				prefix.Reset()
			case ';':
				prefix.Reset()
			default:
				prefix.WriteByte(c)
			}
			i++
		}
	}
	out = append(out, line)
	return out
}
