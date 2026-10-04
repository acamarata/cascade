//go:build capmap

package capmap

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The cascade binary is built once per test binary. cascadeBuilds counts
// the builds so a test can prove it.
var (
	cascadeBuildOnce sync.Once
	cascadeBuildDir  string
	cascadeBinPath   string
	cascadeBuildErr  error
	cascadeBuilds    atomic.Int32
)

// TestMain removes the directory the binary helper built into.
func TestMain(m *testing.M) {
	code := m.Run()
	if cascadeBuildDir != "" {
		_ = os.RemoveAll(cascadeBuildDir)
	}
	os.Exit(code)
}

// moduleRoot returns the directory of the module the tests run in, from
// `go env GOMOD`. It never asks git and never reads a planning directory.
func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "capmap: go env GOMOD")
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", cascade.New(cascade.KindInternal, "capmap: the tests are not running inside a Go module")
	}
	return filepath.Dir(gomod), nil
}

// cascadeBinary returns the path of the cascade binary, built from the
// module under test on the first call and reused after. It fails the test
// if the build fails.
func cascadeBinary(t *testing.T) string {
	t.Helper()
	cascadeBuildOnce.Do(buildCascade)
	if cascadeBuildErr != nil {
		t.Fatal(cascadeBuildErr)
	}
	return cascadeBinPath
}

// buildCascade runs the one `go build` of ./cmd/cascade. It records the
// result in the package variables; callers go through cascadeBinary.
func buildCascade() {
	cascadeBuilds.Add(1)
	root, err := moduleRoot()
	if err != nil {
		cascadeBuildErr = err
		return
	}
	if cascadeBuildDir, err = os.MkdirTemp("", "capmap-bin-"); err != nil {
		cascadeBuildErr = cascade.Wrap(cascade.KindInternal, err, "capmap: make build dir")
		return
	}
	name := "cascade"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(cascadeBuildDir, name)
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/cascade")
	build.Dir = root
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		cascadeBuildErr = cascade.Wrapf(cascade.KindInternal, buildErr, "capmap: go build ./cmd/cascade:\n%s", out)
		return
	}
	cascadeBinPath = bin
}

// isolatedHome makes a fresh home directory and returns it with an
// environment that points every home-relative path at it. The directory is
// short so a unix socket under it fits the platform path limit. Inherited
// home, XDG and cascade variables are dropped, so a probe never touches the
// real home directory, keychain files or config.
func isolatedHome(t *testing.T) (home string, env []string) {
	t.Helper()
	home, err := os.MkdirTemp("", "cm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	set := map[string]string{
		"HOME": home, "USERPROFILE": home, "APPDATA": filepath.Join(home, "appdata"),
		"LOCALAPPDATA": filepath.Join(home, "localappdata"), "CASCADE_HOME": filepath.Join(home, ".cascade"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
	}
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); set[key] == "" {
			env = append(env, kv)
		}
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return home, env
}

// cascadeCommand returns a command that runs the built binary with args,
// inside the isolated home returned alongside it.
func cascadeCommand(t *testing.T, args ...string) (*exec.Cmd, string) {
	t.Helper()
	bin := cascadeBinary(t)
	home, env := isolatedHome(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = home, env
	return cmd, home
}

// TestBinaryHelper_BuildsOnceWithIsolatedHome builds the binary twice
// through the helper, expects one build, runs it, and checks the command's
// environment never carries the real home directory.
func TestBinaryHelper_BuildsOnceWithIsolatedHome(t *testing.T) {
	realHome, _ := os.UserHomeDir()
	first := cascadeBinary(t)
	if second := cascadeBinary(t); second != first {
		t.Fatalf("second call returned %q, first returned %q", second, first)
	}
	if n := cascadeBuilds.Load(); n != 1 {
		t.Fatalf("go build ran %d times, want 1", n)
	}
	cmd, home := cascadeCommand(t, "version")
	if out, err := cmd.CombinedOutput(); err != nil || len(out) == 0 {
		t.Fatalf("cascade version: err=%v output=%q", err, out)
	}
	for _, kv := range cmd.Env {
		if key, val, _ := strings.Cut(kv, "="); (key == "HOME" || key == "USERPROFILE") && (val != home || val == realHome) {
			t.Fatalf("%s=%q, want the isolated home %q", key, val, home)
		}
	}
}
