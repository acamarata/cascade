package config

// Purpose: P1-SEC-30 acceptance [0]-[2] and [6]: the loosening ceremony of
// `config set|unset|edit` against a recording AcceptLoosening seam, and the
// permission preflight of unset and edit.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// acceptRecorder records every AcceptLoosening call and answers with err.
type acceptRecorder struct {
	digests []string
	err     error
}

func (a *acceptRecorder) accept(_ context.Context, sha256hex string) error {
	a.digests = append(a.digests, sha256hex)
	return a.err
}

// looseningRoot is newTestRootWithEnv with an AcceptLoosening seam
// (nil accept leaves Deps.AcceptLoosening nil).
func looseningRoot(t *testing.T, home string, env map[string]string, accept func(context.Context, string) error) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return home
		}
		return env[k]
	}
	paths, err := runtime.NewPathProvider(getenv, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "cascade"}
	root.AddCommand(NewConfigCmd(Deps{Paths: paths, Getenv: getenv, Clock: runtime.NewFixedClock(time.Unix(0, 0)),
		Environ: func() []string { return nil }, AcceptLoosening: accept}))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	return root, &out
}

// runLoosening runs `config <args>` with rec as the seam and returns the
// combined output and error.
func runLoosening(t *testing.T, home string, env map[string]string, rec *acceptRecorder, args ...string) (string, error) {
	t.Helper()
	var accept func(context.Context, string) error
	if rec != nil {
		accept = rec.accept
	}
	root, out := looseningRoot(t, home, env, accept)
	root.SetArgs(append([]string{"config"}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// wantOneAcceptOfFile asserts exactly one call, carrying path's sha256.
func wantOneAcceptOfFile(t *testing.T, label string, rec *acceptRecorder, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if len(rec.digests) != 1 || rec.digests[0] != runtime.ConfigDigest(data) {
		t.Fatalf("%s: AcceptLoosening calls = %v, want exactly [sha256 of the written file %s]", label, rec.digests, runtime.ConfigDigest(data))
	}
}

func TestConfigSetLooseningCallsAcceptLoosening(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"conductor.spill_enabled", "true"},
		// A family a conductor-only stub classifier misses: the compare is
		// unconditional, so this loosening is still accepted.
		{"nodes.trust_tier", `"worker-trusted"`},
	} {
		home, path := secretHome(t, "")
		rec := &acceptRecorder{}
		if out, err := runLoosening(t, home, nil, rec, "set", tc.key, tc.value); err != nil {
			t.Fatalf("set %s: %v\n%s", tc.key, err, out)
		}
		wantOneAcceptOfFile(t, "set "+tc.key, rec, path)
	}
}

func TestConfigSetTighteningNeverCallsAccept(t *testing.T) {
	for _, tc := range []struct{ prior, key, value string }{
		{"[conductor]\nspill_enabled = true\n", "conductor.spill_enabled", "false"},
		{"", "logging.level", `"debug"`},
		{"[nodes]\ntrust_tier = \"worker-trusted\"\n", "nodes.trust_tier", `"worker-trusted"`},
	} {
		home, path := secretHome(t, tc.prior)
		rec := &acceptRecorder{}
		out, err := runLoosening(t, home, nil, rec, "set", tc.key, tc.value)
		if err != nil || len(rec.digests) != 0 || strings.Contains(out, "this change loosens") {
			t.Fatalf("set %s=%s over %q: err=%v accept calls=%d out=%q; a tightening or no-op must not prompt", tc.key, tc.value, tc.prior, err, len(rec.digests), out)
		}
		if data, _ := os.ReadFile(path); !strings.Contains(string(data), strings.Trim(tc.value, `"`)) {
			t.Fatalf("set %s was not written: %q", tc.key, data)
		}
	}
}

func TestConfigEditLooseningCallsAcceptLoosening(t *testing.T) {
	home, path := secretHome(t, "")
	rec := &acceptRecorder{}
	t.Setenv(fakeEditorLineEnv, "[conductor]\nspill_enabled = true\n[nodes]\ntrust_tier = \"worker-trusted\"")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runLoosening(t, home, map[string]string{"EDITOR": self}, rec, "edit")
	if err != nil {
		t.Fatalf("edit: %v\n%s", err, out)
	}
	wantOneAcceptOfFile(t, "edit loosening two families", rec, path)
	if !strings.Contains(out, "conductor.spill_enabled") || !strings.Contains(out, "nodes.trust_tier") {
		t.Fatalf("the loosening notice does not name both families: %q", out)
	}
}

func TestConfigUnsetLooseningCallsAcceptLoosening(t *testing.T) {
	// Defaults are the most restrictive values, so an unset loosens only
	// where any change counts as a loosening (nodes.trust_tier) or where an
	// explicit value is stricter than the default.
	home, path := secretHome(t, "[nodes]\ntrust_tier = \"controller\"\n")
	rec := &acceptRecorder{}
	if out, err := runLoosening(t, home, nil, rec, "unset", "nodes.trust_tier"); err != nil {
		t.Fatalf("unset: %v\n%s", err, out)
	}
	wantOneAcceptOfFile(t, "unset nodes.trust_tier", rec, path)
}

func TestConfigLooseningNoInputRefusesBeforeKeystore(t *testing.T) {
	home, path := secretHome(t, "")
	// The seam answers exactly as callElevated does under CASCADE_NO_INPUT=1
	// (refused before custody opens; the ordering is proven against the real
	// callElevated in cmd/cascade TestAcceptConfigLooseningNoInputRefusesBeforeKeystore).
	rec := &acceptRecorder{err: elevation.ErrNoInput()}
	out, err := runLoosening(t, home, map[string]string{"CASCADE_NO_INPUT": "1"}, rec, "set", "conductor.spill_enabled", "true")
	if !errors.Is(err, elevation.ErrNoInput()) || strings.Contains(out, looseningUnreachableMsg) {
		t.Fatalf("err = %v, out = %q; want the no-input refusal returned, not reported as an unreachable daemon", err, out)
	}
	if !strings.Contains(err.Error(), "--accept-loosening") {
		t.Fatalf("the refusal does not say how to accept later: %v", err)
	}
	wantOneAcceptOfFile(t, "no-input", rec, path)
}

func TestConfigLooseningDaemonUnreachableExitsZero(t *testing.T) {
	for name, rec := range map[string]*acceptRecorder{
		"no daemon client":   nil,
		"daemon unreachable": {err: cascade.New(cascade.KindUnavailable, "dial unix: no such file")},
	} {
		home, path := secretHome(t, "")
		out, err := runLoosening(t, home, nil, rec, "set", "conductor.spill_enabled", "true")
		if err != nil || !strings.Contains(out, looseningUnreachableMsg) {
			t.Fatalf("%s: err=%v out=%q; want exit 0 and the unreachable message", name, err, out)
		}
		if !strings.Contains(out, "applies only after `cascade config reload --accept-loosening`") {
			t.Fatalf("%s: the printed message does not name the accept command: %q", name, out)
		}
		if data, _ := os.ReadFile(path); !strings.Contains(string(data), "spill_enabled = true") {
			t.Fatalf("%s: the file was not written: %q", name, data)
		}
	}
}

func TestConfigUnsetEditRefuseInsecureFile(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	for _, args := range [][]string{{"unset", "conductor.spill_enabled"}, {"edit"}} {
		home := t.TempDir()
		path := filepath.Join(home, "config.toml")
		content := []byte("[conductor]\nspill_enabled = true\n")
		if err := os.WriteFile(path, content, 0o600); err != nil || os.Chmod(path, 0o666) != nil {
			t.Fatal(err)
		}
		t.Setenv(fakeEditorLineEnv, "[logging]\nlevel = \"debug\"")
		self, _ := os.Executable()
		rec := &acceptRecorder{}
		_, err := runLoosening(t, home, map[string]string{"EDITOR": self}, rec, args...)
		if kind, _ := cascade.KindOf(err); kind != cascade.KindPermissionDenied {
			t.Fatalf("%v on a 0666 config: err = %v (kind %q), want permission_denied", args, err, kind)
		}
		data, _ := os.ReadFile(path)
		fi, _ := os.Stat(path)
		if !bytes.Equal(data, content) || fi.Mode().Perm() != 0o666 || len(rec.digests) != 0 {
			t.Fatalf("%v: the refused verb changed the file (mode %v) or called accept", args, fi.Mode().Perm())
		}
	}
}

func TestConfigReloadAcceptLoosening(t *testing.T) {
	home, path := secretHome(t, "[conductor]\nspill_enabled = true\n")
	rec := &acceptRecorder{}
	if out, err := runLoosening(t, home, nil, rec, "reload", "--accept-loosening"); err != nil {
		t.Fatalf("reload --accept-loosening: %v\n%s", err, out)
	}
	wantOneAcceptOfFile(t, "reload --accept-loosening", rec, path)
	if _, err := runLoosening(t, home, nil, nil, "reload", "--accept-loosening"); err == nil {
		t.Fatal("reload --accept-loosening with no daemon client succeeded")
	}
}

// TestConfigLooseningComparesUnloadablePreviousAsDefaults pins R6: a previous
// file that no longer loads is compared as the defaults, never skipped, so a
// guarded value it already held still goes through the ceremony.
func TestConfigLooseningComparesUnloadablePreviousAsDefaults(t *testing.T) {
	home, path := secretHome(t, "[conductor]\nspill_enabled = true\n[retrieval.fusion]\nk = \"not-a-number\"\n")
	rec := &acceptRecorder{}
	if out, err := runLoosening(t, home, nil, rec, "set", "retrieval.fusion.k", "60"); err != nil {
		t.Fatalf("set over an unloadable file: %v\n%s", err, out)
	}
	wantOneAcceptOfFile(t, "unloadable previous file", rec, path)
}
