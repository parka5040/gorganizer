package ipc

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestErrorTokensMatchGuiMessages(t *testing.T) {
	cppPath := filepath.Join("..", "..", "src", "ui", "InstallErrorText.cpp")
	cpp, err := os.ReadFile(cppPath)
	if os.IsNotExist(err) {
		t.Skipf("GUI error messages not found at %s: %v", cppPath, err)
	}
	if err != nil {
		t.Fatalf("reading GUI error messages %s: %v", cppPath, err)
	}

	messageBlock := regexp.MustCompile(`(?s)QString knownTokenMessage\(.*?\n\}`).Find(cpp)
	escapedBlock := regexp.MustCompile(`(?s)bool tokenValuesPercentEscaped\(.*?\n\}`).Find(cpp)
	if messageBlock == nil || escapedBlock == nil {
		t.Fatal("could not locate GUI error message or percent-escape function")
	}

	messagePattern := regexp.MustCompile(`token\s*==\s*QLatin1String\("([a-z][a-z0-9_]*)"\)`)
	messages := make(map[string]bool)
	for _, match := range messagePattern.FindAllSubmatch(messageBlock, -1) {
		messages[string(match[1])] = true
	}
	if len(messages) == 0 {
		t.Fatal("parsed zero GUI token messages")
	}

	registered := make(map[string]bool, len(errorTokens))
	allowWithoutMessage := map[string]bool{"fomod_required": true}
	for _, token := range errorTokens {
		name := strings.TrimSuffix(token, ":")
		registered[name] = true
		if !messages[name] && !allowWithoutMessage[name] {
			t.Errorf("registered error token %q has no GUI message", name)
		}
	}

	escapedPattern := regexp.MustCompile(`QStringLiteral\("([a-z][a-z0-9_]*)"\)`)
	escaped := escapedPattern.FindAllSubmatch(escapedBlock, -1)
	if len(escaped) == 0 {
		t.Fatal("parsed zero GUI percent-escaped tokens")
	}
	for _, match := range escaped {
		name := string(match[1])
		if !registered[name] {
			t.Errorf("GUI percent-decodes unregistered error token %q", name)
		}
	}
}
