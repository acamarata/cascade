// Purpose: tests for `cascade self-update`: the channel no-ops leave the
//
//	binary untouched and exit 0, and --check --json emits a CheckResult
//	whose compatibility is null. Everything lives under t.TempDir().
//
// SPORT: cmd/cascade self-update/ADDED.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/update"
)

// refusingFetcher fails the test on any fetch: the no-op paths fetch nothing.
type refusingFetcher struct{ t *testing.T }

func (f refusingFetcher) Get(context.Context, string) (io.ReadCloser, error) {
	f.t.Error("a no-op channel fetched a release file")
	return nil, errors.New("no fetch expected")
}

// noopDeps returns deps for an executable at exe with stamp, plus the
// executable's sha256 so a test can prove it is unchanged.
func noopDeps(t *testing.T, exe, stamp string) func() (update.Deps, error) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("installed binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() (update.Deps, error) {
		return update.Deps{
			Env:     update.Env{Stamp: stamp, Executable: func() (string, error) { return exe, nil }, ReceiptPath: filepath.Join(t.TempDir(), "none.json")},
			Version: "2.0.0",
			Fetcher: refusingFetcher{t},
			TrustRoot: func() (minisign.PublicKey, error) {
				t.Error("a no-op channel loaded the trust root")
				return minisign.PublicKey{}, nil
			},
		}, nil
	}
}

func fileSum(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func runSelfUpdate(t *testing.T, deps func() (update.Deps, error), args ...string) (string, error) {
	t.Helper()
	orig := selfUpdateDeps
	t.Cleanup(func() { selfUpdateDeps = orig })
	selfUpdateDeps = deps
	return execRoot(t, append([]string{"self-update"}, args...)...)
}

func TestSelfUpdateNoOpUnderCaskroom(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "opt", "homebrew", "Caskroom", "cascade", "2.0.0", "cascade")
	deps := noopDeps(t, exe, "")
	before := fileSum(t, exe)
	out, err := runSelfUpdate(t, deps)
	if err != nil || !strings.Contains(out, "managed by brew; run brew upgrade cascade") {
		t.Fatalf("self-update under a Caskroom = %q, %v", out, err)
	}
	if fileSum(t, exe) != before {
		t.Fatal("self-update changed a brew-managed binary")
	}
}

func TestSelfUpdateNoOpForOCI(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "usr", "local", "bin", "cascade")
	deps := noopDeps(t, exe, "oci")
	before := fileSum(t, exe)
	out, err := runSelfUpdate(t, deps)
	if err != nil || !strings.Contains(out, "managed by oci; pull the image") {
		t.Fatalf("self-update for oci = %q, %v", out, err)
	}
	if fileSum(t, exe) != before {
		t.Fatal("self-update changed an OCI binary")
	}
	out, err = runSelfUpdate(t, noopDeps(t, exe, "node-managed"))
	if err != nil || !strings.Contains(out, "managed by the controller (node upgrade)") {
		t.Fatalf("self-update for node-managed = %q, %v", out, err)
	}
	out, err = runSelfUpdate(t, noopDeps(t, exe, ""))
	if err != nil || !strings.Contains(out, "installed manually") {
		t.Fatalf("self-update for a manual install = %q, %v", out, err)
	}
}

func TestSelfUpdateCheckJSON(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "bin", "cascade")
	out, err := runSelfUpdate(t, noopDeps(t, exe, "brew"), "--check", "--json")
	if err != nil {
		t.Fatalf("--check --json: %v", err)
	}
	var env struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil || !env.OK {
		t.Fatalf("envelope %q: %v", out, err)
	}
	var res update.CheckResult
	if err := json.Unmarshal(env.Data, &res); err != nil || res.Action != update.ActionNoopChannel || res.Compatibility != nil {
		t.Fatalf("data = %s (%v)", env.Data, err)
	}
	var generic map[string]any
	if err := json.Unmarshal(env.Data, &generic); err != nil {
		t.Fatal(err)
	}
	if v, ok := generic["compatibility"]; !ok || v != nil {
		t.Fatalf("data %s does not carry compatibility null", env.Data)
	}
}

func TestSelfUpdateViews(t *testing.T) {
	base := update.CheckResult{Channel: update.ChannelScript, Current: update.Ident{Version: "2.0.0"},
		Latest: update.Ident{Version: "2.0.1", ReleaseTag: "v2.0.1"}}
	upd := base
	upd.Action = update.ActionUpdate
	none := base
	none.Action = update.ActionNone
	for got, want := range map[string]string{
		checkView{upd}.String():  "update available: 2.0.0 -> 2.0.1 (v2.0.1)",
		checkView{none}.String(): "is up to date (latest v2.0.1)",
		resultView{update.Result{CheckResult: upd, Updated: true, DaemonSignalled: true, PreviousDir: "/p"}}.String(): "the daemon relaunched on 2.0.1",
		resultView{update.Result{CheckResult: none}}.String():                                                         "is up to date",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view %q does not contain %q", got, want)
		}
	}
	if _, err := productionSelfUpdateDeps(); err != nil {
		t.Fatalf("production deps under the test HOME: %v", err)
	}
}
