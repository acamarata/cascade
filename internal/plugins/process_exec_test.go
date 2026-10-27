// Purpose: ResolveExecutable, ExecutableDigest and SetExecutableDirForTest:
// only a regular, not group- or world-writable file named after the plugin
// beside the cascade binary (or in a test-injected dir) resolves, and the
// injection hook panics in any binary that is not a test.
// Inputs: fake executables under t.TempDir(); a probe binary the test builds.
// SPORT: internal/plugins process executable resolution tests (P1-PLG-10).
package plugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// exeName is id with the platform's executable suffix.
func exeName(id string) string {
	if goruntime.GOOS == "windows" {
		return id + ".exe"
	}
	return id
}

// injectDir points ResolveExecutable at a fresh dir for the test.
func injectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(SetExecutableDirForTest(dir))
	return dir
}

func writeExecutable(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fakeExecutable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func requireExecutableRefused(t *testing.T, label, reason string) {
	t.Helper()
	path, err := ResolveExecutable("probe")
	if err == nil {
		t.Fatalf("%s: resolved %s, want a refusal", label, path)
	}
	requireRefusal(t, err, cascade.KindUnavailable, errExecutableRefused, "("+reason+")")
}

func TestResolveExecutableUsesInjectedDir(t *testing.T) {
	dir := injectDir(t)
	want := filepath.Join(dir, exeName("probe"))
	writeExecutable(t, want, 0o755)
	got, err := ResolveExecutable("probe")
	if err != nil || got != want {
		t.Fatalf("ResolveExecutable = %q, %v; want %q", got, err, want)
	}
	digest, err := ExecutableDigest(got)
	if err != nil || digest != sha256Hex([]byte(fakeExecutable)) {
		t.Fatalf("ExecutableDigest = %q, %v; want the sha256 of the fake executable", digest, err)
	}
	// Without the injection, the lookup is beside this test binary.
	restore := SetExecutableDirForTest("")
	defer restore()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	_, err = ResolveExecutable("probe")
	if err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("uninjected ResolveExecutable = %v, want a refusal for %s", err, filepath.Join(filepath.Dir(self), "probe"))
	}
}

func TestResolveExecutableRefusesNonRegular(t *testing.T) {
	dir := injectDir(t)
	path := filepath.Join(dir, exeName("probe"))
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	requireExecutableRefused(t, "a directory", reasonIrregular)
	if goruntime.GOOS == "windows" {
		return // no fifo or unprivileged symlink to build here
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v: %s", err, out)
	}
	if err := os.Symlink(fifo, path); err != nil {
		t.Fatal(err)
	}
	requireExecutableRefused(t, "a symlink to a fifo", reasonIrregular)
	// A symlink to a well-formed executable is not one either.
	target := filepath.Join(dir, "target")
	writeExecutable(t, target, 0o755)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	requireExecutableRefused(t, "a symlink to a regular file", reasonIrregular)
	if _, err := ExecutableDigest(path); err == nil {
		t.Fatal("ExecutableDigest pinned a symlink")
	}
}

func TestResolveExecutableRefusesWritable(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the group/world-writable check is unix-only")
	}
	dir := injectDir(t)
	path := filepath.Join(dir, "probe")
	writeExecutable(t, path, 0o755)
	if _, err := ResolveExecutable("probe"); err != nil {
		t.Fatalf("0o755 refused: %v", err)
	}
	for _, mode := range []os.FileMode{0o775, 0o757} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		requireExecutableRefused(t, mode.String(), reasonWritable)
		if _, err := ExecutableDigest(path); err == nil {
			t.Fatalf("ExecutableDigest pinned a %s file", mode)
		}
	}
}

// probeMain calls the injection hook from an ordinary (non-test) binary.
const probeMain = `package main

import "github.com/acamarata/cascade/internal/plugins"

func main() {
	plugins.SetExecutableDirForTest("injected")
	println("SetExecutableDirForTest returned")
}
`

// TestSetExecutableDirForTestPanicsOutsideTests builds a non-test binary that
// calls the hook (through a go build overlay, so nothing is written into the
// tree) and runs it.
func TestSetExecutableDirForTestPanicsOutsideTests(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go toolchain not on PATH: %v", err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(src, []byte(probeMain), 0o600); err != nil {
		t.Fatal(err)
	}
	virtual := filepath.Join(root, "internal", "plugins", "zzexecdirprobe", "main.go")
	overlay, _ := json.Marshal(map[string]map[string]string{"Replace": {virtual: src}})
	overlayPath := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	bin := filepath.Join(tmp, exeName("probe"))
	build := exec.CommandContext(ctx, goBin, "build", "-overlay", overlayPath, "-o", bin, "./internal/plugins/zzexecdirprobe")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the probe: %v\n%s", err, out)
	}
	run := exec.CommandContext(ctx, bin)
	run.Env = []string{"HOME=" + tmp, "USERPROFILE=" + tmp, "CASCADE_HOME=" + tmp}
	out, err := run.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "plugins: SetExecutableDirForTest outside a test binary") ||
		strings.Contains(string(out), "returned") {
		t.Fatalf("probe = %v\n%s\nwant a panic naming the hook", err, out)
	}
}

// TestResolveExecutableIDMatchesManifestRule: an id is accepted exactly when
// pkg/plugin.Validate accepts it as a manifest id; any other is
// KindInvalidInput and never names a file.
func TestResolveExecutableIDMatchesManifestRule(t *testing.T) {
	injectDir(t) // empty: a valid id is refused only as missing
	for _, id := range []string{"probe", "a-1", "z9", "", ".", "..", "../probe", `a\b`, "foo.bar", ".hidden", "C:evil", "Probe", "1abc", "a_b", "-a"} {
		manifestOK := true
		for _, ve := range plugin.Validate(plugin.Manifest{ID: id}) {
			if ve.Field == "id" {
				manifestOK = false
			}
		}
		_, err := ResolveExecutable(id)
		kind, _ := cascade.KindOf(err)
		if want := map[bool]cascade.Kind{true: cascade.KindUnavailable, false: cascade.KindInvalidInput}[manifestOK]; kind != want {
			t.Fatalf("ResolveExecutable(%q) kind = %v (%v), want %v (manifest id valid: %v)", id, kind, err, want, manifestOK)
		}
	}
}

// TestExecutableDigestRefusesASwappedFile: a file replaced between the Lstat
// and the Open is refused, never pinned.
func TestExecutableDigestRefusesASwappedFile(t *testing.T) {
	dir := injectDir(t)
	path := filepath.Join(dir, exeName("probe"))
	writeExecutable(t, path, 0o755)
	other := filepath.Join(dir, "other")
	writeExecutable(t, other, 0o755)
	beforeDigestOpen = func(p string) {
		if err := os.Rename(other, p); err != nil {
			t.Errorf("swap: %v", err)
		}
	}
	t.Cleanup(func() { beforeDigestOpen = nil })
	_, err := ExecutableDigest(path)
	requireRefusal(t, err, cascade.KindUnavailable, "("+reasonSwapped+")")
}
