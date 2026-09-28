package launchertest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const immutableNotice = "This system keeps its system files read-only, so Gorganizer will not install developer tools on it. To build Gorganizer here, open a Distrobox or Toolbox container, run ./gorganizer.sh inside it, and start Gorganizer from that container."

// prepareSetupFixture installs failing package-manager shims and an isolated operating-system release file.
func prepareSetupFixture(t *testing.T, release string, ostree bool) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	writeFixtureFile(t, filepath.Join(f.root, "os-release"), []byte(release), 0o644)
	if ostree {
		writeFixtureFile(t, filepath.Join(f.root, "ostree-marker"), nil, 0o600)
	}
	calls := filepath.Join(f.root, "package-calls")
	for _, name := range []string{"sudo", "pacman", "dnf", "apt-get", "apt-cache", "dpkg", "zypper", "rpm-ostree", "steamos-readonly", "nix-env"} {
		writeFixtureFile(t, filepath.Join(f.shims, name), []byte("#!/bin/sh\nprintf '%s %s\\n' '"+name+"' \"$*\" >> \"$PACKAGE_CALLS\"\nexit 86\n"), 0o755)
	}
	return f, calls
}

// assertNoPackageCalls checks that a launcher invocation did not reach a system package tool.
func assertNoPackageCalls(t *testing.T, path string) {
	t.Helper()
	if calls, err := os.ReadFile(path); err == nil && len(calls) > 0 {
		t.Fatalf("system package tools called: %s", calls)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// removeFixtureBinaries makes an installed fixture follow the first-run build path.
func removeFixtureBinaries(t *testing.T, f *fixture) {
	t.Helper()
	for _, name := range []string{"gorganizerd", "build/src/gorganizer"} {
		if err := os.Remove(filepath.Join(f.root, name)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestImmutableSetupAndFirstRunStopWithoutPackageTools checks four read-only releases refuse incomplete toolchains before building.
func TestImmutableSetupAndFirstRunStopWithoutPackageTools(t *testing.T) {
	for _, tc := range []struct {
		name, release string
		ostree        bool
	}{
		{"SteamOS", "ID=steamos\nID_LIKE=arch\n", false},
		{"Bazzite", "ID=bazzite\nID_LIKE=fedora\n", true},
		{"Silverblue", "ID=fedora\nVARIANT_ID=silverblue\n", false},
		{"NixOS", "ID=nixos\n", false},
		{"Bluefin", "ID=bluefin\nID_LIKE=fedora\n", false},
		{"Aurora", "ID=aurora\nID_LIKE=fedora\n", false},
		{"Endless", "ID=endless\n", false},
		{"Steamos-like", "ID=custom\nID_LIKE=\"steamos arch\"\n", false},
		{"ostree marker", "ID=fedora\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := prepareSetupFixture(t, tc.release, tc.ostree)
			settings := []string{"PACKAGE_CALLS=" + calls, "FAKE_HEADERS_MISSING=protobuf"}
			for _, flow := range []struct{ name, command string }{
				{"setup", "cmd_setup"},
				{"first run", "cmd_install"},
			} {
				t.Run(flow.name, func(t *testing.T) {
					if flow.name == "first run" {
						removeFixtureBinaries(t, f)
					}
					output, err := f.run(t, flow.command, settings...)
					if err == nil || strings.Count(output, immutableNotice) != 1 || !strings.Contains(output, "Missing build tools: protobuf development headers") {
						t.Fatalf("%s: %v: %q", flow.name, err, output)
					}
					assertNoPackageCalls(t, calls)
					if _, err := os.Stat(f.log); !os.IsNotExist(err) {
						t.Fatalf("build ran with missing headers: %v", err)
					}
				})
			}
		})
	}
}

// TestImmutableSetupAndFirstRunContinueWithTools checks both flows accept an existing toolchain without package commands.
func TestImmutableSetupAndFirstRunContinueWithTools(t *testing.T) {
	for _, tc := range []struct {
		name, release string
		ostree        bool
	}{
		{"SteamOS", "ID=steamos\nID_LIKE=arch\n", false},
		{"Bazzite", "ID=bazzite\nID_LIKE=fedora\n", true},
		{"Silverblue", "ID=fedora\nVARIANT_ID=silverblue\n", false},
		{"NixOS", "ID=nixos\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := prepareSetupFixture(t, tc.release, tc.ostree)
			settings := []string{"PACKAGE_CALLS=" + calls}
			output, err := f.run(t, "cmd_setup", settings...)
			if err != nil || strings.Count(output, immutableNotice) != 1 {
				t.Fatalf("setup: %v: %q", err, output)
			}
			removeFixtureBinaries(t, f)
			output, err = f.run(t, "needs_register() { return 1; }; cmd_install", settings...)
			if err != nil || strings.Count(output, immutableNotice) != 1 || !strings.Contains(output, "Build complete.") {
				t.Fatalf("first run: %v: %q", err, output)
			}
			assertNoPackageCalls(t, calls)
			if got := string(readFixtureFile(t, f.log)); !strings.Contains(got, " all gui\ntoolchain local\n") {
				t.Fatalf("build was not started: %q", got)
			}
			if got := string(readFixtureFile(t, filepath.Join(f.root, "go.log"))); !strings.Contains(got, "go local\n") || strings.Contains(got, "<unset>") {
				t.Fatalf("Go toolchain setting: %q", got)
			}
		})
	}
}

// TestImmutableVariantsAndDoctor checks variant and ostree detection also keep optional runtime advice package-free.
func TestImmutableVariantsAndDoctor(t *testing.T) {
	for _, variant := range []string{"kinoite", "sericea", "onyx", "atomic", "coreos"} {
		t.Run(variant, func(t *testing.T) {
			f, calls := prepareSetupFixture(t, "ID=fedora\nVARIANT_ID="+variant+"\n", false)
			output, err := f.run(t, "cmd_doctor", "PACKAGE_CALLS="+calls)
			if err != nil || strings.Count(output, immutableNotice) != 1 || strings.Contains(output, "sudo") || strings.Contains(output, "dnf") {
				t.Fatalf("doctor: %v: %q", err, output)
			}
			assertNoPackageCalls(t, calls)
		})
	}
}

// TestMutableSetupRequiresInteractiveConsent checks package suggestions never trigger sudo without a terminal.
func TestMutableSetupRequiresInteractiveConsent(t *testing.T) {
	for _, tc := range []struct{ name, release, query, install string }{
		{"Arch", "ID=arch\n", "pacman", "sudo pacman -S --needed"},
		{"Ubuntu", "ID=ubuntu\nID_LIKE=debian\n", "apt-cache", "sudo apt-get install -y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := prepareSetupFixture(t, tc.release, false)
			writeFixtureFile(t, filepath.Join(f.shims, tc.query), []byte("#!/bin/sh\nprintf '%s %s\\n' '"+tc.query+"' \"$*\" >> \"$PACKAGE_CALLS\"\nexit 0\n"), 0o755)
			if tc.query == "pacman" {
				writeFixtureFile(t, filepath.Join(f.shims, "pacman"), []byte("#!/bin/sh\nprintf 'pacman %s\\n' \"$*\" >> \"$PACKAGE_CALLS\"\n[ \"$1\" = -Si ]\n"), 0o755)
			}
			removeFixtureBinaries(t, f)
			output, err := f.run(t, "cmd_install", "PACKAGE_CALLS="+calls)
			if err == nil || !strings.Contains(output, tc.install) || !strings.Contains(output, "Gorganizer cannot build because the needed tools were not installed.") {
				t.Fatalf("non-interactive install: %v: %q", err, output)
			}
			if got := string(readFixtureFile(t, calls)); !strings.Contains(got, tc.query+" ") || strings.Contains(got, "sudo ") {
				t.Fatalf("package calls: %q", got)
			}
			if _, err := os.Stat(f.log); !os.IsNotExist(err) {
				t.Fatalf("build ran after declined install: %v", err)
			}
		})
	}
}

// TestDeclinedInteractiveInstallStopsBeforeBuild checks an explicit no at the package prompt leaves binaries untouched.
func TestDeclinedInteractiveInstallStopsBeforeBuild(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script is needed for a terminal-backed prompt")
	}
	f, calls := prepareSetupFixture(t, "ID=arch\n", false)
	writeFixtureFile(t, filepath.Join(f.shims, "pacman"), []byte("#!/bin/sh\nprintf 'pacman %s\\n' \"$*\" >> \"$PACKAGE_CALLS\"\n[ \"$1\" = -Si ]\n"), 0o755)
	removeFixtureBinaries(t, f)
	output, err := f.runTerminal(t, "cmd_install", "n\n", "PACKAGE_CALLS="+calls)
	if err == nil || !strings.Contains(output, "Gorganizer cannot build because the needed tools were not installed.") {
		t.Fatalf("declined prompt: %v: %q", err, output)
	}
	if got := string(readFixtureFile(t, calls)); strings.Contains(got, "sudo ") {
		t.Fatalf("sudo called: %q", got)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("build ran after a no answer: %v", err)
	}
}

// TestFailedInteractiveInstallStopsBeforeBuild checks an unsuccessful consented install leaves binaries untouched.
func TestFailedInteractiveInstallStopsBeforeBuild(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script is needed for a terminal-backed prompt")
	}
	f, calls := prepareSetupFixture(t, "ID=arch\n", false)
	writeFixtureFile(t, filepath.Join(f.shims, "pacman"), []byte("#!/bin/sh\nprintf 'pacman %s\\n' \"$*\" >> \"$PACKAGE_CALLS\"\n[ \"$1\" = -Si ]\n"), 0o755)
	writeFixtureFile(t, filepath.Join(f.shims, "sudo"), []byte("#!/bin/sh\nprintf 'sudo %s\\n' \"$*\" >> \"$PACKAGE_CALLS\"\n[ \"$1\" = -v ]\n"), 0o755)
	removeFixtureBinaries(t, f)
	output, err := f.runTerminal(t, "cmd_install", "y\n", "PACKAGE_CALLS="+calls)
	if err == nil || !strings.Contains(output, "Package install failed. Build stopped.") {
		t.Fatalf("failed package install: %v: %q", err, output)
	}
	if got := string(readFixtureFile(t, calls)); !strings.Contains(got, "sudo -v\n") || !strings.Contains(got, "sudo pacman -S --needed") {
		t.Fatalf("package calls: %q", got)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("build ran after failed install: %v", err)
	}
}

// TestLocalGoVersionGuard checks full numeric versions and prevents network toolchain switching.
func TestLocalGoVersionGuard(t *testing.T) {
	for _, tc := range []struct {
		version  string
		accepted bool
	}{
		{"1.26.1", false},
		{"1.26.2", true},
		{"1.26.10", true},
		{"1.27.0", true},
		{"1.27.1-X:nodwarf5", true},
		{"1.27rc1", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			f := newFixture(t)
			output, err := f.run(t, "do_build", "FAKE_GO_VERSION="+tc.version, "GOTOOLCHAIN=auto")
			if (err == nil) != tc.accepted {
				t.Fatalf("Go %s build: %v: %q", tc.version, err, output)
			}
			if !tc.accepted {
				want := "Gorganizer needs Go 1.26.2 or newer, but this system has Go 1.26.1. Install a newer Go from https://go.dev/dl/ and run ./gorganizer.sh again."
				if !strings.Contains(output, want) {
					t.Errorf("version error: %q", output)
				}
				if _, err := os.Stat(f.log); !os.IsNotExist(err) {
					t.Errorf("build ran with old Go: %v", err)
				}
			} else if !strings.Contains(string(readFixtureFile(t, f.log)), " all gui\ntoolchain local\n") {
				t.Fatal("build did not run")
			}
			if got := string(readFixtureFile(t, filepath.Join(f.root, "go.log"))); !strings.Contains(got, "go local\n") || strings.Contains(got, "<unset>") {
				t.Errorf("Go toolchain setting: %q", got)
			}
		})
	}
}

// TestRequiredGoVersionComesFromModule checks the launcher's minimum version follows go.mod.
func TestRequiredGoVersionComesFromModule(t *testing.T) {
	f := newFixture(t)
	writeFixtureFile(t, filepath.Join(f.root, "go.mod"), []byte("module fake\n\ngo 1.26.10\n"), 0o644)
	output, err := f.run(t, "do_build", "FAKE_GO_VERSION=1.26.9")
	if err == nil || !strings.Contains(output, "Gorganizer needs Go 1.26.10 or newer, but this system has Go 1.26.9.") {
		t.Fatalf("go.mod minimum: %v: %q", err, output)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("build ran with Go below go.mod: %v", err)
	}
}

// TestRequiredBuildToolsAreReported checks each missing build command is named without starting a build.
func TestRequiredBuildToolsAreReported(t *testing.T) {
	for _, tc := range []struct{ missing, want string }{
		{"make", "make"},
		{"cmake", "cmake"},
		{"go", "go"},
		{"protoc", "protoc"},
		{"grpc_cpp_plugin", "grpc_cpp_plugin"},
		{"pkg-config pkgconf", "pkg-config or pkgconf"},
		{"c++ g++ clang++", "C++ compiler"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f, calls := prepareSetupFixture(t, "ID=nixos\n", false)
			output, err := f.run(t, `command() {
    if [ "${1:-}" = -v ]; then
        case " ${FAKE_MISSING_TOOLS:-} " in *" $2 "*) return 1 ;; esac
    fi
    builtin command "$@"
}; cmd_setup`, "PACKAGE_CALLS="+calls, "FAKE_MISSING_TOOLS="+tc.missing)
			if err == nil || !strings.Contains(output, "Missing build tools: "+tc.want) {
				t.Fatalf("missing command: %v: %q", err, output)
			}
			assertNoPackageCalls(t, calls)
		})
	}
}

// TestFamilyDependenciesIncludeMakeAndPkgConfig checks every supported family supplies the essential build packages.
func TestFamilyDependenciesIncludeMakeAndPkgConfig(t *testing.T) {
	f := newFixture(t)
	for _, family := range []string{"arch", "debian", "fedora", "suse"} {
		t.Run(family, func(t *testing.T) {
			output, err := f.run(t, "deps_for_family "+family)
			if err != nil || !strings.Contains(output, "make|make\n") || !strings.Contains(output, "pkg-config|") || strings.Contains(output, "python3") {
				t.Fatalf("%s dependencies: %v: %q", family, err, output)
			}
		})
	}
}

// TestMissingBuildCommandsAndHeaders checks dependency diagnostics name missing build inputs before make runs.
func TestMissingBuildCommandsAndHeaders(t *testing.T) {
	f, calls := prepareSetupFixture(t, "ID=nixos\n", false)
	output, err := f.run(t, `command() {
    if [ "${1:-}" = -v ] && [ "${2:-}" = protoc ]; then return 1; fi
    builtin command "$@"
}; cmd_setup`, "PACKAGE_CALLS="+calls, "FAKE_HEADERS_MISSING=grpc++")
	if err == nil || !strings.Contains(output, "protoc") || !strings.Contains(output, "gRPC development headers") {
		t.Fatalf("missing inputs: %v: %q", err, output)
	}
	assertNoPackageCalls(t, calls)
}

// TestDevelGoVersionIsParsed checks a development toolchain's version line is understood.
func TestDevelGoVersionIsParsed(t *testing.T) {
	f := newFixture(t)
	output, err := f.run(t, "check_go_version_warning", "FAKE_GO_OUTPUT=go version devel go1.28-abc123 Mon Jan 1 00:00:00 2026 +0000 linux/amd64")
	if err != nil {
		t.Fatalf("devel toolchain refused: %v: %q", err, output)
	}
}
