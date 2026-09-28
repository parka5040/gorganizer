package desktop

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestExecValueGolden checks every reserved path character and literal percent in desktop commands.
func TestExecValueGolden(t *testing.T) {
	for _, tc := range []struct {
		name, path, want string
	}{
		{"plain", "/share/gorganizer/bin/gorganizer", `/share/gorganizer/bin/gorganizer launch`},
		{"spaces", `/home/My Mods/gorganizer`, `"/home/My Mods/gorganizer" launch`},
		{"quotes", `/home/My "Mods"/gorganizer`, `"/home/My \\"Mods\\"/gorganizer" launch`},
		{"apostrophe", `/home/it's/gorganizer`, `"/home/it's/gorganizer" launch`},
		{"backslash", `/home/a\b/gorganizer`, `"/home/a\\\\b/gorganizer" launch`},
		{"dollar", `/home/$HOME/gorganizer`, `"/home/\\$HOME/gorganizer" launch`},
		{"backtick", "/home/`cmd`/gorganizer", "\"/home/\\\\`cmd\\\\`/gorganizer\" launch"},
		{"percent", `/home/100%/gorganizer`, `/home/100%%/gorganizer launch`},
		{"non-ASCII", `/home/日本語/gorganizer`, `/home/日本語/gorganizer launch`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExecValue([]string{tc.path, "launch"}, false); got != tc.want {
				t.Errorf("ExecValue = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderedEntriesGolden checks full desktop entries for plain and escaped paths.
func TestRenderedEntriesGolden(t *testing.T) {
	for _, tc := range []struct {
		name, launcher, icon, app, nxm string
	}{
		{
			name: "plain", launcher: "/share/gorganizer/bin/gorganizer", icon: "/share/icon.png",
			app: "[Desktop Entry]\nType=Application\nName=Gorganizer\nComment=Native Linux mod organizer for Bethesda games\nExec=/share/gorganizer/bin/gorganizer launch\nIcon=/share/icon.png\nTerminal=false\nCategories=Game;Utility;\nKeywords=mod;organizer;skyrim;fallout;bethesda;stardew;smapi;\nVersion=1.5\n",
			nxm: "[Desktop Entry]\nType=Application\nName=Gorganizer NXM Handler\nComment=Nexus Mods download handler for Gorganizer\nExec=/share/gorganizer/bin/gorganizer nxm %u\nIcon=/share/icon.png\nTerminal=false\nCategories=Game;\nNoDisplay=true\nMimeType=x-scheme-handler/nxm;\nVersion=1.5\n",
		},
		{
			name: "escaped", launcher: `/share/My "Mods" $dir/100%\x/gorganizer`, icon: `/share/My "Mods" $dir/100%\x/icon.png`,
			app: "[Desktop Entry]\nType=Application\nName=Gorganizer\nComment=Native Linux mod organizer for Bethesda games\nExec=\"/share/My \\\\\"Mods\\\\\" \\\\$dir/100%%\\\\\\\\x/gorganizer\" launch\nIcon=/share/My \"Mods\" $dir/100%\\\\x/icon.png\nTerminal=false\nCategories=Game;Utility;\nKeywords=mod;organizer;skyrim;fallout;bethesda;stardew;smapi;\nVersion=1.5\n",
			nxm: "[Desktop Entry]\nType=Application\nName=Gorganizer NXM Handler\nComment=Nexus Mods download handler for Gorganizer\nExec=\"/share/My \\\\\"Mods\\\\\" \\\\$dir/100%%\\\\\\\\x/gorganizer\" nxm %u\nIcon=/share/My \"Mods\" $dir/100%\\\\x/icon.png\nTerminal=false\nCategories=Game;\nNoDisplay=true\nMimeType=x-scheme-handler/nxm;\nVersion=1.5\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderApplication(tc.launcher, tc.icon); got != tc.app {
				t.Errorf("application:\n%s\nwant:\n%s", got, tc.app)
			}
			if got := RenderNXM(tc.launcher, tc.icon); got != tc.nxm {
				t.Errorf("handler:\n%s\nwant:\n%s", got, tc.nxm)
			}
		})
	}
}

// unescapeDesktopValue decodes a general Desktop Entry value before Exec parsing.
func unescapeDesktopValue(value string) (string, error) {
	var decoded strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			decoded.WriteByte(value[i])
			continue
		}
		i++
		if i == len(value) {
			return "", fmt.Errorf("trailing general escape")
		}
		switch value[i] {
		case 's':
			decoded.WriteByte(' ')
		case 'n':
			decoded.WriteByte('\n')
		case 't':
			decoded.WriteByte('\t')
		case 'r':
			decoded.WriteByte('\r')
		case '\\':
			decoded.WriteByte('\\')
		default:
			return "", fmt.Errorf("invalid general escape %q", value[i])
		}
	}
	return decoded.String(), nil
}

// parseDesktopExec decodes quoted Exec arguments and field codes after general unescaping.
func parseDesktopExec(value string) ([]string, error) {
	decoded, err := unescapeDesktopValue(value)
	if err != nil {
		return nil, err
	}
	var args []string
	var arg strings.Builder
	quoted, active := false, false
	for i := 0; i < len(decoded); i++ {
		switch decoded[i] {
		case ' ':
			if !quoted {
				if active {
					args = append(args, arg.String())
					arg.Reset()
					active = false
				}
				continue
			}
		case '"':
			quoted, active = !quoted, true
			continue
		case '\\':
			if quoted {
				i++
				if i == len(decoded) || !strings.ContainsRune("\"`$\\", rune(decoded[i])) {
					return nil, fmt.Errorf("invalid quoted escape")
				}
			}
		case '%':
			if i+1 == len(decoded) {
				return nil, fmt.Errorf("incomplete field code")
			}
			if decoded[i+1] == '%' {
				i++
			} else if decoded[i+1] != 'u' {
				return nil, fmt.Errorf("invalid field code")
			} else {
				i++
				arg.WriteString("%u")
				active = true
				continue
			}
		}
		arg.WriteByte(decoded[i])
		active = true
	}
	if quoted {
		return nil, fmt.Errorf("unclosed quote")
	}
	if active {
		args = append(args, arg.String())
	}
	return args, nil
}

// TestRenderedEntriesRoundTrip parses application and NXM commands and icons in spec order.
func TestRenderedEntriesRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, launcher, icon string
	}{
		{"plain", "/share/gorganizer/bin/gorganizer", "/share/icon.png"},
		{"awkward", `/share/My "Mods" $dir/100%\x/gorganizer`, `/share/My "Mods" $dir/100%\x/icon.png`},
		{"escaped control characters", "/share/a\tb\nc\rd\\s/gorganizer", " /share/a\tb\nc\rd\\s/icon.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, entry := range []struct {
				body string
				want []string
			}{
				{RenderApplication(tc.launcher, tc.icon), []string{tc.launcher, "launch"}},
				{RenderNXM(tc.launcher, tc.icon), []string{tc.launcher, "nxm", "%u"}},
			} {
				var execValue, iconValue string
				for _, line := range strings.Split(entry.body, "\n") {
					if value, ok := strings.CutPrefix(line, "Exec="); ok {
						execValue = value
					}
					if value, ok := strings.CutPrefix(line, "Icon="); ok {
						iconValue = value
					}
				}
				got, err := parseDesktopExec(execValue)
				if err != nil || !reflect.DeepEqual(got, entry.want) {
					t.Errorf("Exec %q parses as %q, %v; want %q", execValue, got, err, entry.want)
				}
				icon, err := unescapeDesktopValue(iconValue)
				if err != nil || icon != tc.icon {
					t.Errorf("Icon %q parses as %q, %v; want %q", iconValue, icon, err, tc.icon)
				}
				if tc.name == "plain" && !reflect.DeepEqual(strings.Fields(execValue), entry.want) {
					t.Errorf("plain Exec does not split into arguments: %q", execValue)
				}
			}
		})
	}
}

// TestOwnsEntryRequiresDesktopSection checks unrelated sections cannot authorize removal.
func TestOwnsEntryRequiresDesktopSection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	launcher := filepath.Join(root, "gorganizer", "bin", "gorganizer")
	if err := WriteLauncher(launcher, root); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{
		"[Other]\nName=Gorganizer\nExec=" + ExecValue([]string{launcher, "launch"}, false) + "\n",
		"[Desktop Entry]\nName=Other program\nExec=" + ExecValue([]string{launcher, "launch"}, false) + "\n",
	} {
		if OwnsEntry("gorganizer.desktop", []byte(entry), root, launcher) {
			t.Errorf("accepted another application's entry: %q", entry)
		}
	}
}

// TestUnregisterInterimEscaping removes entries that escaped every Exec space.
func TestUnregisterInterimEscaping(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "My Data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	checkout := filepath.Join(root, "My Mods")
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "gorganizer.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := EnvironmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := Register(paths, checkout, paths.Icon); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		path, name, action string
		uri                bool
	}{
		{paths.Application, "gorganizer.desktop", "launch", false},
		{paths.NXM, Handler, "nxm", true},
	} {
		body, err := os.ReadFile(entry.path)
		if err != nil {
			t.Fatal(err)
		}
		newExec := ExecValue([]string{paths.Launcher, entry.action}, entry.uri)
		interimExec := strings.ReplaceAll(newExec, " ", `\s`)
		body = bytes.Replace(body, []byte("Exec="+newExec+"\n"), []byte("Exec="+interimExec+"\n"), 1)
		if err := os.WriteFile(entry.path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		if !OwnsEntry(entry.name, body, checkout, paths.Launcher) {
			t.Errorf("interim entry not recognized: %s", entry.name)
		}
	}
	if err := Unregister(paths, checkout); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Application, paths.NXM, paths.Launcher} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("interim registration remained at %s: %v", path, err)
		}
	}
	settings, err := os.ReadFile(paths.Mimeapps)
	if err != nil || strings.Contains(string(settings), Handler) {
		t.Errorf("NXM association remained: %q, %v", settings, err)
	}
}

// TestLauncherQuotesCheckout executes an apostrophe-containing checkout and forwards arguments unchanged.
func TestLauncherQuotesCheckout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	checkout := filepath.Join(root, "someone's mods")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(checkout, "gorganizer.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "bin", "gorganizer")
	if err := WriteLauncher(launcher, checkout); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(launcher)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("launcher mode = %v, %v", info, err)
	}
	cmd := exec.Command("/bin/sh", launcher, "nxm", "a b", "100%", "$HOME")
	got, err := cmd.CombinedOutput()
	if err != nil || string(got) != "nxm\na b\n100%\n$HOME\n" {
		t.Fatalf("forwarding = %q, %v", got, err)
	}
}

// TestLauncherExplainsMovedCheckout checks the recovery hint without notification tools.
func TestLauncherExplainsMovedCheckout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	checkout := filepath.Join(root, "moved")
	launcher := filepath.Join(root, "bin", "gorganizer")
	if err := WriteLauncher(launcher, checkout); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", launcher)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "empty"))
	got, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(err.Error(), "exit status 1") || string(got) != movedMessage+"\n" {
		t.Fatalf("missing checkout = %q, %v", got, err)
	}
}

// TestEditMimeappsPreservesUnrelatedContent checks additions, updates, repeat calls and selective removal.
func TestEditMimeappsPreservesUnrelatedContent(t *testing.T) {
	for _, tc := range []struct{ name, before, registered, removed string }{
		{"fresh", "", "[Default Applications]\n" + mimeKey + Handler + ";\n\n[Added Associations]\n" + mimeKey + Handler + ";\n", "[Default Applications]\n\n[Added Associations]\n"},
		{"no final newline", "[Added Associations]\ncomment=keep", "[Added Associations]\ncomment=keep\n" + mimeKey + Handler + ";\n\n[Default Applications]\n" + mimeKey + Handler + ";\n", "[Added Associations]\ncomment=keep\n\n[Default Applications]\n"},
		{"other handlers", "# keep\n[Default Applications]\n" + mimeKey + "other.desktop;\nother/type=app.desktop;\n\n[Added Associations]\n# keep this\n" + mimeKey + "first.desktop;second.desktop;\n[Unrelated]\nkeep = exactly\n",
			"# keep\n[Default Applications]\n" + mimeKey + Handler + ";\nother/type=app.desktop;\n\n[Added Associations]\n# keep this\n" + mimeKey + Handler + ";first.desktop;second.desktop;\n[Unrelated]\nkeep = exactly\n",
			"# keep\n[Default Applications]\nother/type=app.desktop;\n\n[Added Associations]\n# keep this\n" + mimeKey + "first.desktop;second.desktop;\n[Unrelated]\nkeep = exactly\n"},
		{"duplicate associations", "[Default Applications]\n" + mimeKey + "other.desktop;\n[Added Associations]\n" + mimeKey + "first.desktop;second.desktop;\n" + mimeKey + "second.desktop;third.desktop;\n",
			"[Default Applications]\n" + mimeKey + Handler + ";\n[Added Associations]\n" + mimeKey + Handler + ";first.desktop;second.desktop;third.desktop;\n",
			"[Default Applications]\n[Added Associations]\n" + mimeKey + "first.desktop;second.desktop;third.desktop;\n"},
		{"other default", "[Default Applications]\n" + mimeKey + "other.desktop;\n[Added Associations]\n" + mimeKey + Handler + ";other.desktop;\n",
			"[Default Applications]\n" + mimeKey + Handler + ";\n[Added Associations]\n" + mimeKey + Handler + ";other.desktop;\n",
			"[Default Applications]\n[Added Associations]\n" + mimeKey + "other.desktop;\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EditMimeapps([]byte(tc.before), true)
			if err != nil || string(got) != tc.registered {
				t.Fatalf("registered = %q, %v; want %q", got, err, tc.registered)
			}
			again, err := EditMimeapps(got, true)
			if err != nil || !bytes.Equal(got, again) {
				t.Fatalf("repeat = %q, %v", again, err)
			}
			removed, err := EditMimeapps(got, false)
			if err != nil || string(removed) != tc.removed {
				t.Fatalf("removed = %q, %v; want %q", removed, err, tc.removed)
			}
		})
	}
}

// TestEditMimeappsRejectsInvalidInput checks invalid UTF-8 and oversized settings remain unchanged.
func TestEditMimeappsRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"invalid UTF-8", []byte{0xff, 0xfe}},
		{"oversized", bytes.Repeat([]byte("a"), maxMimeappsSize+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			path := filepath.Join(dir, "mimeapps.list")
			if err := os.WriteFile(path, tc.body, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, register := range []bool{true, false} {
				if err := UpdateMimeapps(path, register); err == nil {
					t.Fatal("expected refusal")
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, tc.body) {
					t.Fatalf("settings changed: %v, %v", got, err)
				}
			}
		})
	}
}
