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

type cppLine struct {
	num          int
	hasCode      bool
	commentStart bool
	blockComment bool
}

// checkCppFile reports comment-policy violations in one C++ file via a small lexer.
func checkCppFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return 1
	}
	lines := lexCpp(string(data))
	raw := strings.Split(string(data), "\n")
	count := 0
	report := func(n int, kind string) {
		fmt.Printf("%s:%d: forbidden comment (%s)\n", path, n, kind)
		count++
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if l.blockComment {
			report(l.num, "block")
			continue
		}
		if !l.commentStart {
			continue
		}
		if l.hasCode {
			report(l.num, "trailing")
			continue
		}
		if i+1 < len(lines) && lines[i+1].commentStart && !lines[i+1].hasCode {
			report(l.num, "multi-line")
			continue
		}
		next := ""
		for j := i + 1; j < len(raw); j++ {
			if strings.TrimSpace(raw[j]) != "" {
				next = raw[j]
				break
			}
		}
		if !strings.Contains(next, "(") {
			report(l.num, "non-header")
		}
	}
	return count
}

// lexCpp classifies each line, tracking string/char/raw-string/block-comment state.
func lexCpp(src string) []cppLine {
	var out []cppLine
	line := cppLine{num: 1}
	inBlock, inStr, inChar, inRaw := false, false, false, false
	rawDelim := ""
	i := 0
	flush := func() {
		out = append(out, line)
		line = cppLine{num: line.num + 1}
	}
	for i < len(src) {
		c := src[i]
		if c == '\n' {
			if inBlock {
				line.blockComment = line.blockComment || line.commentStart || true
			}
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
						i += 1 + m + 1
						continue
					}
				}
			}
			inStr = true
			line.hasCode = true
			i++
		case c == '\'':
			inChar = true
			line.hasCode = true
			i++
		default:
			if c != ' ' && c != '\t' && c != '\r' {
				line.hasCode = true
			}
			i++
		}
	}
	out = append(out, line)
	return out
}
