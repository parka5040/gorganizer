package packagingtest

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// packagingScript returns the location of a release packaging script.
func packagingScript(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "../..", "packaging", name)
}

// runScript runs a shell script with isolated user and temporary directories.
func runScript(t *testing.T, script string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestReleaseTarIsIndependentOfWorkingDirectory checks matching input trees produce identical archives.
func TestReleaseTarIsIndependentOfWorkingDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := t.TempDir()
	second := t.TempDir()
	var archives [2][32]byte
	for n, root := range []string{first, second} {
		tree := filepath.Join(root, "gorganizer-fixture")
		if err := os.MkdirAll(filepath.Join(tree, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, "bin", "app"), []byte("same release"), 0o755); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(root, "bundle.tar.gz")
		cmd := exec.Command("bash", "-c", `source "$1"; release_tar "$2" "$3" 1700000000`, "bash", packagingScript(t, "common.sh"), tree, archive)
		cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "TMPDIR="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tar: %v: %s", err, out)
		}
		data, err := os.ReadFile(archive)
		if err != nil {
			t.Fatal(err)
		}
		archives[n] = sha256.Sum256(data)
	}
	if archives[0] != archives[1] {
		t.Fatalf("archives differ: %x vs %x", archives[0], archives[1])
	}
}

// TestAuditReleaseRejectsUnsafeFiles checks static binaries, missing libraries, RUNPATHs and forbidden paths.
func TestAuditReleaseRejectsUnsafeFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tool := range []string{"cc", "readelf", "strings", "patchelf"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable", tool)
		}
	}
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	static := filepath.Join(root, "static")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", static, source)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off", "HOME="+t.TempDir(), "TMPDIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Go fixture: %v: %s", err, out)
	}
	cSource := filepath.Join(root, "main.c")
	if err := os.WriteFile(cSource, []byte("int main(void) { return 0; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, rpath, content, want string
		includeStatic              bool
	}{
		{name: "static binaries", includeStatic: true},
		{name: "valid GUI", rpath: "$ORIGIN/../lib"},
		{name: "missing RUNPATH", want: "RUNPATH"},
		{name: "wrong RUNPATH", rpath: "$ORIGIN", want: "RUNPATH"},
		{name: "legacy RPATH", rpath: "$ORIGIN/../lib", want: "RPATH"},
		{name: "missing NEEDED library", rpath: "$ORIGIN/../lib", want: "unresolved libMissing.so"},
		{name: "forbidden build path", rpath: "$ORIGIN/../lib", content: "/opt/qt/private", want: "forbidden"},
		{name: "embedded work directory", rpath: "$ORIGIN/../lib", content: "/build-fixture/work", want: "build directory"},
		{name: "forbidden home path", rpath: "$ORIGIN/../lib", content: "/home/", want: "forbidden"},
		{name: "dynamic maintenance binary", rpath: "$ORIGIN/../lib", includeStatic: true, want: "not static"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := t.TempDir()
			if err := os.MkdirAll(filepath.Join(bundle, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.includeStatic {
				for _, name := range []string{"gorganizerd", "gorganizerctl"} {
					data, err := os.ReadFile(static)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(bundle, "bin", name), data, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.name != "static binaries" {
				args := []string{cSource, "-o", filepath.Join(bundle, "bin", "gorganizer-gui")}
				if tc.name == "missing NEEDED library" {
					missingSource := filepath.Join(root, "missing.c")
					if err := os.WriteFile(missingSource, []byte("int missing(void) { return 1; }\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					missingMain := filepath.Join(t.TempDir(), "missing-main.c")
					if err := os.WriteFile(missingMain, []byte("extern int missing(void); int main(void) { return missing(); }\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					args[0] = missingMain
					lib := exec.Command("cc", "-shared", "-fPIC", "-Wl,-soname,libMissing.so", missingSource, "-o", filepath.Join(root, "libMissing.so"))
					if out, err := lib.CombinedOutput(); err != nil {
						t.Fatalf("shared fixture: %v: %s", err, out)
					}
					args = append(args, "-L"+root, "-lMissing")
				}
				if tc.rpath != "" {
					if tc.name == "legacy RPATH" {
						args = append(args, "-Wl,--disable-new-dtags")
					}
					args = append(args, "-Wl,-rpath,"+tc.rpath)
				}
				cmd := exec.Command("cc", args...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("C fixture: %v: %s", err, out)
				}
			}
			if tc.name == "dynamic maintenance binary" {
				data, err := os.ReadFile(filepath.Join(bundle, "bin", "gorganizer-gui"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bundle, "bin", "gorganizerctl"), data, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.content != "" {
				if err := os.WriteFile(filepath.Join(bundle, "sample.txt"), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "embedded work directory" {
				t.Setenv("RELEASE_BUILD_DIR", "/build-fixture")
			}
			out, err := runScript(t, packagingScript(t, "audit-release.sh"), bundle)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(out, tc.want)) {
				t.Fatalf("audit = %v: %s", err, out)
			}
		})
	}
}
