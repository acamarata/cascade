package config

// Purpose: proves `config validate` and `config set`, which reach
//   config.toml outside runtime.Load, apply the same permission policy
//   through runtime.CheckConfigPermissions, fail-closed: an unsafe file is
//   never reported valid and never written, and a file that cannot be
//   inspected stops both verbs.
// Inputs: real config.toml files with real modes under t.TempDir(); the
//   standard test root (CASCADE_HOME pointed at the temp dir).
// Outputs: n/a (test-only).
// Constraints: unix modes only (windows reports not_checked and skips). The
//   unreadable-parent case needs a non-root user, since root reads a 0000
//   directory.
// SPORT: cmd/cascade/config (ADD, P1-CORE-16).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// permVerbFixture writes config.toml with mode under a fresh home and
// returns the home and the file path.
func permVerbFixture(t *testing.T, mode os.FileMode) (home, path string) {
	t.Helper()
	for _, k := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(k, t.TempDir())
	}
	home = filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("schema_version = 1\n"), 0o600); err != nil || os.Chmod(path, mode) != nil {
		t.Fatalf("fixture: %v", err)
	}
	return home, path
}

// fileState is a file's content digest and mode.
type fileState struct {
	sum  [32]byte
	mode os.FileMode
}

func stateOf(t *testing.T, path string) fileState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileState{sum: sha256.Sum256(data), mode: fi.Mode()}
}

// runVerb runs `cascade config <args>` against home and returns stdout
// (which, under the test root, also holds the warnings) and the error.
func runVerb(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	root, out, errOut := newTestRoot(t, home)
	root.SetArgs(append([]string{"config"}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String() + errOut.String(), err
}

func wantPermDenied(t *testing.T, verb string, err error, out string) {
	t.Helper()
	kind, ok := cascade.KindOf(err)
	if err == nil || !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("%s err = %v (kind %v, typed %v), want KindPermissionDenied; output=%q", verb, err, kind, ok, out)
	}
	if got := cascade.ExitCode(err); got != cascade.ExitPermissionDenied {
		t.Errorf("%s exit code = %d, want %d", verb, got, cascade.ExitPermissionDenied)
	}
	if strings.Contains(out, "is valid") {
		t.Errorf("%s output %q reports the config valid", verb, out)
	}
}

func TestConfigVerbsRefuseUnsafePerms(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows: config permissions are not checked")
	}
	t.Run("0666 validate refused", func(t *testing.T) {
		home, path := permVerbFixture(t, 0o666)
		out, err := runVerb(t, home, "validate")
		wantPermDenied(t, "validate", err, out)
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("refusal %q must name %s and the fix", err, path)
		}
	})
	t.Run("0666 set refused and nothing written", func(t *testing.T) {
		home, path := permVerbFixture(t, 0o666)
		before := stateOf(t, path)
		out, err := runVerb(t, home, "set", "logging.level", `"debug"`)
		wantPermDenied(t, "set", err, out)
		if after := stateOf(t, path); after != before {
			t.Errorf("set changed the refused file: mode %v -> %v, content changed %v", before.mode, after.mode, after.sum != before.sum)
		}
	})
	t.Run("0600 validates and sets", func(t *testing.T) {
		home, path := permVerbFixture(t, 0o600)
		if out, err := runVerb(t, home, "validate"); err != nil || !strings.Contains(out, "is valid") {
			t.Fatalf("validate on 0600: err=%v output=%q", err, out)
		}
		if out, err := runVerb(t, home, "set", "logging.level", `"debug"`); err != nil {
			t.Fatalf("set on 0600: %v (output %q)", err, out)
		}
		if data, err := os.ReadFile(path); err != nil || !bytes.Contains(data, []byte("debug")) {
			t.Errorf("set did not write the value: %q, %v", data, err)
		}
	})
	t.Run("symlink in a 0777 dir refused by validate and set", testVerbsRefuseLinkInOpenDir)
	t.Run("unreadable-parent", testVerbsUnreadableParent)
}

// testVerbsRefuseLinkInOpenDir proves both verbs judge the directory that
// holds a symlinked config.toml: a link in a 0777 directory to a 0600 file
// in a safe directory is refused, and the target is left unchanged.
func testVerbsRefuseLinkInOpenDir(t *testing.T) {
	_, target := permVerbFixture(t, 0o600)
	home := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(home, 0o700); err != nil || os.Chmod(home, 0o777) != nil || os.Symlink(target, filepath.Join(home, "config.toml")) != nil {
		t.Fatalf("fixture: %v", err)
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	before := stateOf(t, target)
	for _, args := range [][]string{{"validate"}, {"set", "logging.level", `"debug"`}} {
		out, err := runVerb(t, home, args...)
		wantPermDenied(t, args[0], err, out)
		if !strings.Contains(err.Error(), "chmod o-w "+realHome) {
			t.Errorf("%s refusal %q does not name the link directory fix", args[0], err)
		}
	}
	if after := stateOf(t, target); after != before {
		t.Errorf("a refused verb changed the symlink target")
	}
}

// testVerbsUnreadableParent proves an inspection error stops both verbs
// (fail-closed): with the parent directory at 0000 neither verb proceeds,
// and after a chmod-back the file is byte-identical.
func testVerbsUnreadableParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("unreadable-parent needs a non-root user: root reads a 0000 directory")
	}
	home, path := permVerbFixture(t, 0o600)
	before := stateOf(t, path)
	if err := os.Chmod(home, 0); err != nil {
		t.Fatal(err)
	}
	restore := func() { _ = os.Chmod(home, 0o700) }
	t.Cleanup(restore)
	for _, args := range [][]string{{"validate"}, {"set", "logging.level", `"debug"`}} {
		out, err := runVerb(t, home, args...)
		wantPermDenied(t, args[0], err, out)
		if !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s err %q does not name the stat error", args[0], err)
		}
	}
	restore()
	if after := stateOf(t, path); after != before {
		t.Errorf("a refused verb changed the file behind an unreadable parent")
	}
}
