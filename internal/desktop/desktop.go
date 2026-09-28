package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const Handler = "gorganizer-nxm.desktop"
const movedMessage = "Gorganizer was moved or removed. Open a terminal in its new folder and run: ./gorganizer.sh register"

type Paths struct {
	Application string
	NXM         string
	Launcher    string
	Mimeapps    string
	Icon        string
}

// EnvironmentPaths resolves desktop registration locations using the XDG home directories.
func EnvironmentPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("finding your home folder: %w", err)
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return Paths{
		Application: filepath.Join(data, "applications", "gorganizer.desktop"),
		NXM:         filepath.Join(data, "applications", Handler),
		Launcher:    filepath.Join(data, "gorganizer", "bin", "gorganizer"),
		Mimeapps:    filepath.Join(config, "mimeapps.list"),
		Icon:        filepath.Join(data, "icons", "hicolor", "256x256", "apps", "gorganizer.png"),
	}, nil
}

// EscapeValue escapes a Desktop Entry general string value.
func EscapeValue(value string) string {
	var out strings.Builder
	for i, r := range value {
		switch r {
		case '\\':
			out.WriteString(`\\`)
		case ' ':
			if i == 0 {
				out.WriteString(`\s`)
			} else {
				out.WriteRune(r)
			}
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		case '\r':
			out.WriteString(`\r`)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// ExecValue quotes and escapes command arguments while allowing only the requested URI field code.
func ExecValue(args []string, uri bool) string {
	var parts []string
	for _, arg := range args {
		quoted := strings.ContainsAny(arg, " \t\n\r\"'\\><~|&;$*?#()`")
		var encoded strings.Builder
		if quoted {
			encoded.WriteByte('"')
		}
		for _, r := range arg {
			if r == '%' {
				encoded.WriteString("%%")
				continue
			}
			if quoted && (r == '"' || r == '`' || r == '$' || r == '\\') {
				encoded.WriteByte('\\')
			}
			encoded.WriteRune(r)
		}
		if quoted {
			encoded.WriteByte('"')
		}
		parts = append(parts, encoded.String())
	}
	if uri {
		parts = append(parts, "%u")
	}
	return EscapeValue(strings.Join(parts, " "))
}

// RenderApplication returns the desktop launcher entry.
func RenderApplication(launcher, icon string) string {
	return "[Desktop Entry]\nType=Application\nName=Gorganizer\nComment=Native Linux mod organizer for Bethesda games\nExec=" + ExecValue([]string{launcher, "launch"}, false) + "\nIcon=" + EscapeValue(icon) + "\nTerminal=false\nCategories=Game;Utility;\nKeywords=mod;organizer;skyrim;fallout;bethesda;stardew;smapi;\nVersion=1.5\n"
}

// RenderNXM returns the Nexus download handler entry.
func RenderNXM(launcher, icon string) string {
	return "[Desktop Entry]\nType=Application\nName=Gorganizer NXM Handler\nComment=Nexus Mods download handler for Gorganizer\nExec=" + ExecValue([]string{launcher, "nxm"}, true) + "\nIcon=" + EscapeValue(icon) + "\nTerminal=false\nCategories=Game;\nNoDisplay=true\nMimeType=x-scheme-handler/nxm;\nVersion=1.5\n"
}

// RenderLauncher returns a POSIX shell launcher bound to one checkout.
func RenderLauncher(checkout string) string {
	script := filepath.Join(checkout, "gorganizer.sh")
	quoted := "'" + strings.ReplaceAll(script, "'", "'\\''") + "'"
	return "#!/bin/sh\n" +
		"if [ ! -f " + quoted + " ]; then\n" +
		"    message='" + movedMessage + "'\n" +
		"    printf '%s\\n' \"$message\" >&2\n" +
		"    if command -v notify-send >/dev/null 2>&1; then\n" +
		"        notify-send 'Gorganizer' \"$message\" || :\n" +
		"    fi\n" +
		"    exit 1\n" +
		"fi\n" +
		"exec " + quoted + " \"$@\"\n"
}

// WriteLauncher publishes an executable launcher for the checkout.
func WriteLauncher(path, checkout string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating launcher folder: %w", err)
	}
	if err := atomicfile.WriteFile(path, []byte(RenderLauncher(checkout)), 0o755); err != nil {
		return fmt.Errorf("writing launcher: %w", err)
	}
	return nil
}

// OwnsEntry identifies legacy and stable desktop entries belonging to a checkout.
func OwnsEntry(name string, body []byte, checkout, launcher string) bool {
	action, label := "launch", "Gorganizer"
	if name == Handler {
		action, label = "nxm %u", "Gorganizer NXM Handler"
	} else if name != "gorganizer.desktop" {
		return false
	}
	oldExec := filepath.Join(checkout, "gorganizer.sh") + " " + action
	newExec := ExecValue([]string{launcher, strings.Fields(action)[0]}, name == Handler)
	section := ""
	nameValue, execValue := "", ""
	nameCount, execCount, sectionCount := 0, 0, 0
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			if section == "[Desktop Entry]" {
				sectionCount++
			}
			continue
		}
		if section != "[Desktop Entry]" {
			continue
		}
		if strings.HasPrefix(line, "Name=") {
			nameValue = strings.TrimPrefix(line, "Name=")
			nameCount++
		}
		if strings.HasPrefix(line, "Exec=") {
			execValue = strings.TrimPrefix(line, "Exec=")
			execCount++
		}
	}
	if sectionCount != 1 || nameCount != 1 || execCount != 1 || nameValue != label {
		return false
	}
	if execValue == oldExec {
		return true
	}
	if execValue != newExec && execValue != strings.ReplaceAll(newExec, " ", `\s`) {
		return false
	}
	owned, err := LauncherBelongsTo(launcher, checkout)
	return err == nil && owned
}
