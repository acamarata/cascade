//go:build integration

package init_test

// Purpose: the small platform and filesystem helpers the acceptance
//   scenarios share (P1-E16-W4-S35-T5), kept apart from the fixture so
//   both files stay under the 300-line cap.
// SPORT: internal/runtime/init acceptance (ADD) — P1-E16-W4-S35-T5.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// exeSuffix is the built binary's extension on this platform.
func exeSuffix() string {
	if goruntime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// repoRoot walks up from this package to the module root, so `go build`
// runs inside the module regardless of where the test binary was invoked.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// passthroughEnv carries the few variables the Go toolchain and the OS
// need for a child process to run at all.
//
// An allow-list, not the whole environment: the point of these scenarios
// is that setup works on a machine that has never seen cascade, and
// inheriting the developer's own variables is how a suite quietly starts
// depending on one.
func passthroughEnv() []string {
	var out []string
	for _, key := range []string{
		"PATHEXT", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "COMSPEC",
		"GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT",
	} {
		if v := os.Getenv(key); v != "" {
			out = append(out, key+"="+v)
		}
	}
	return out
}

// exitCodeOf turns a child's error into an exit code, failing the test on
// anything that is not a normal exit — a binary that could not be started
// is a different problem from one that ran and refused.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("running the cascade binary: %v", err)
	return -1
}

// mustMkdirAll creates dir or fails the test.
func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
}

// mustWrite writes body to path or fails the test.
func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// mustRead reads path or fails the test.
func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(raw)
}

// exists reports whether path is present.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// requireContains fails the test when haystack does not contain needle,
// printing the whole haystack — a scenario's output is the evidence.
func requireContains(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%s: expected %q in:\n%s", what, needle, haystack)
	}
}

// removeHarness deletes the seeded harness install, for the scenario that
// asserts what setup does on a machine without one.
func removeHarness(t *testing.T, e env) {
	t.Helper()
	for _, p := range []string{filepath.Join(e.home, ".claude"), e.userConfig()} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatalf("removing %s: %v", p, err)
		}
	}
}
