// Purpose (this file): the bound cascade-nself ConfigApplier's authority
//
//	limits (P1-E25-W5-S103-T1, R-14.322): it pins the owner and admits only
//	plugins.cascade-nself.* (never .managed) and runtime.profile. Every
//	other path refuses the whole diff and leaves config.toml byte-identical.
//
// Inputs: n/a (test-only; CASCADE_HOME/CASCADE_CONFIG point at t.TempDir()).
// Outputs: n/a (test-only).
// Constraints: the applier under test is the one init() binds.
// SPORT: internal/plugins:nself-wiring (TEST) — P1-E25-W5-S103-T1.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	nselfplugin "github.com/acamarata/cascade/plugins/nself"
)

// nselfApplierSeed is real content, so byte-identical is meaningful.
const nselfApplierSeed = "[runtime]\nprofile = \"local\"\n"

// seededNselfConfig points the runtime path provider at a fresh config
// holding nselfApplierSeed and returns its path.
func seededNselfConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", dir)
	t.Setenv("CASCADE_CONFIG", path)
	if err := os.WriteFile(path, []byte(nselfApplierSeed), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return path
}

func TestNselfApplierRefusesForeignPath(t *testing.T) {
	for _, foreign := range []string{
		"registry.pubkey_path",
		"hooks.id.x",
		"plugins.other.x",
		"plugins.cascade-nself.managed.x",
		"plugins.cascade-nself.managed",
		"conductor.external_routing_enabled",
	} {
		t.Run(foreign, func(t *testing.T) {
			path := seededNselfConfig(t)
			_, err := nselfApplierUnderTest.ApplyDiff(context.Background(), "cascade-nself", []nselfplugin.ConfigEntry{
				{Path: "plugins.cascade-nself.project_dir", Literal: `"/p"`},
				{Path: foreign, Literal: `"/tmp/evil.pub"`},
			})
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
				t.Fatalf("ApplyDiff(%s) err = %v, want KindPolicyDenied", foreign, err)
			}
			if err != nil && (!strings.Contains(err.Error(), foreign) || !strings.Contains(err.Error(), "refusing the whole diff")) {
				t.Fatalf("err = %v, want it to name %q", err, foreign)
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil || string(data) != nselfApplierSeed {
				t.Fatalf("config.toml changed by a refused diff (%v):\n%s", rerr, data)
			}
		})
	}
}

// TestNselfApplierPinsOwner is the review's foreign-owner proof: whatever
// owner the caller passes, ownership is recorded under cascade-nself.
func TestNselfApplierPinsOwner(t *testing.T) {
	path := seededNselfConfig(t)
	_, err := nselfApplierUnderTest.ApplyDiff(context.Background(), "evil-owner", []nselfplugin.ConfigEntry{
		{Path: "plugins.cascade-nself.project_dir", Literal: `"/p"`},
	})
	if err != nil {
		t.Fatalf("ApplyDiff: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "evil-owner") {
		t.Fatalf("the caller's owner reached config.toml:\n%s", data)
	}
	if !strings.Contains(string(data), "managed = {") {
		t.Fatalf("no managed record under [plugins.cascade-nself]:\n%s", data)
	}
}

// nselfApplierUnderTest is the applier init() binds.
var nselfApplierUnderTest nselfplugin.ConfigApplier = nselfConfigApplier{}

// TestNselfWiringBindsConfigApplier proves init() installed a REAL
// ConfigApplier (P1-E25-W5-S103-T1), not the plugin's refusing default:
// an ApplyDiff call against a t.TempDir() config.toml actually writes.
func TestNselfWiringBindsConfigApplier(t *testing.T) {
	loadedRegistry(t) // forces this package's init() to have run

	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", dir)
	t.Setenv("CASCADE_CONFIG", filepath.Join(dir, "config.toml"))

	applier := nselfConfigApplier{}
	result, err := applier.ApplyDiff(context.Background(), "cascade-nself", []nselfplugin.ConfigEntry{
		{Path: "runtime.profile", Literal: `"server"`},
	})
	if err != nil {
		t.Fatalf("ApplyDiff over the real applier: %v", err)
	}
	if len(result.Applied) != 1 || result.Applied[0].Path != "runtime.profile" {
		t.Fatalf("result = %+v, want runtime.profile applied", result)
	}
	data, rerr := os.ReadFile(filepath.Join(dir, "config.toml"))
	if rerr != nil {
		t.Fatalf("read config.toml: %v", rerr)
	}
	if !bytes.Contains(data, []byte(`profile = "server"`)) {
		t.Fatalf("config.toml = %s, want the applied runtime.profile line", data)
	}
}

// fakeNselfOnPath writes a POSIX `nself` stand-in answering `version
// --json` and `config get POSTGRES_HOST` (host), puts it first and alone
// on PATH, and returns an nself project directory (the bare t.TempDir(),
// an ordinary absolute path the value screen must accept as project_dir).
// It is a shell script under t.TempDir(), not the nself CLI.
func fakeNselfOnPath(t *testing.T, host string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the nself stand-in is a POSIX shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1 $2 $3\" in\n" +
		"\"version --json \") printf '%s' '{\"version\":\"1.3.5\"}' ;;\n" +
		"\"config get POSTGRES_HOST\") printf '%s' '" + host + "' ;;\n" +
		"*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "nself"), []byte(script), 0o700); err != nil {
		t.Fatalf("write nself stand-in: %v", err)
	}
	t.Setenv("PATH", bin)
	for _, name := range []string{"CASCADE_STORAGE_POSTGRES_DSN", "CASCADE_STORAGE_REDIS_URL",
		"CASCADE_STORAGE_S3_ENDPOINT", "CASCADE_STORAGE_S3_BUCKET", "CASCADE_STORAGE_S3_KEY_ID", "CASCADE_STORAGE_S3_SECRET"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".nself"), 0o755); err != nil {
		t.Fatalf("seed marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nself", "build-version"), []byte("1"), 0o600); err != nil {
		t.Fatalf("seed marker file: %v", err)
	}
	return dir
}

// proposeThroughEgress runs nself_add_cascade through the loaded plugin and
// the REAL egress interceptor init() bound, returning the emitted bytes.
func proposeThroughEgress(t *testing.T, dir string) string {
	t.Helper()
	input, err := json.Marshal(map[string]string{"root_dir": dir})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	got, _ := loadedRegistry(t).Get("cascade-nself")
	out, err := got.Handlers.DispatchTool(context.Background(), "nself_add_cascade", input)
	if err != nil {
		t.Fatalf("DispatchTool(nself_add_cascade): %v", err)
	}
	if len(out) == 0 || !strings.Contains(string(out), "project_dir") {
		t.Fatalf("egress output lacks the proposal (guard against empty output): %s", out)
	}
	return string(out)
}

// TestNselfSplitSecretNeverLeavesEgress: a known-prefix key cut by a
// space, a tab or several spaces is withheld by the production binding
// (the runtime value screen) before the response reaches egress, and the
// APPLY command writes every other entry but never the key.
func TestNselfSplitSecretNeverLeavesEgress(t *testing.T) {
	head, tail := "sk-"+"live-"+"abcdefghijkl", "mnopqrstuvwx"
	for name, sep := range map[string]string{"one space": " ", "tab": "\t", "several spaces": "   "} {
		t.Run(name, func(t *testing.T) {
			cfg := seededNselfConfig(t)
			dir := fakeNselfOnPath(t, head+sep+tail)
			out := proposeThroughEgress(t, dir)
			if !strings.Contains(out, `"withheld":["plugins.cascade-nself.postgres_host"]`) {
				t.Fatalf("egress output does not withhold postgres_host: %s", out)
			}
			for _, leak := range []string{"abcdefghijkl", tail} {
				if strings.Contains(out, leak) {
					t.Fatalf("egress output carries %q: %s", leak, out)
				}
			}
			got, _ := loadedRegistry(t).Get("cascade-nself")
			if err := got.Handlers.RunCommand(context.Background(), "handshake", []string{"--dir", dir, "--json"}); err != nil {
				t.Fatalf("handshake apply: %v", err)
			}
			data, err := os.ReadFile(cfg)
			if err != nil || !strings.Contains(string(data), "project_dir") {
				t.Fatalf("apply wrote no project_dir (%v):\n%s", err, data)
			}
			for _, leak := range []string{"postgres_host", "abcdefghijkl", tail} {
				if strings.Contains(string(data), leak) {
					t.Fatalf("config.toml carries %q:\n%s", leak, data)
				}
			}
		})
	}
}

// TestNselfPlainHostLeavesEgress is the no-false-positive leg: an ordinary
// host is proposed through the same production path.
func TestNselfPlainHostLeavesEgress(t *testing.T) {
	seededNselfConfig(t)
	out := proposeThroughEgress(t, fakeNselfOnPath(t, "db.internal"))
	want := `{"path":"plugins.cascade-nself.postgres_host","literal":"\"db.internal\""}`
	if !strings.Contains(out, want) || strings.Contains(out, "withheld") {
		t.Fatalf("egress output = %s, want %s proposed and nothing withheld", out, want)
	}
}

// TestNselfApplierRefusesWhenHomeUnresolvable proves the applier resolves
// the config path per call and fails closed, as KindInternal with the
// wrapped cause, when neither CASCADE_HOME nor a home directory exists.
func TestNselfApplierRefusesWhenHomeUnresolvable(t *testing.T) {
	t.Setenv("CASCADE_HOME", "")
	t.Setenv("CASCADE_CONFIG", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	res, err := nselfApplierUnderTest.ApplyDiff(context.Background(), "cascade-nself", []nselfplugin.ConfigEntry{
		{Path: "plugins.cascade-nself.project_dir", Literal: `"/p"`},
	})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Fatalf("ApplyDiff err = %v, want KindInternal", err)
	}
	if !strings.Contains(err.Error(), "plugin: cascade-nself resolve config path") ||
		!strings.Contains(err.Error(), "runtime: resolve home directory") {
		t.Fatalf("err = %q, want the wrap prefix and the home-directory cause", err)
	}
	if len(res.Applied)+len(res.Unchanged)+len(res.Skipped) != 0 {
		t.Fatalf("result = %+v, want the zero result on error", res)
	}
}

// TestNselfApplierSurfacesWriterRefusal proves a refusal from the runtime
// writer (here an unparseable existing config.toml) is returned as is and
// leaves the file byte-identical.
func TestNselfApplierSurfacesWriterRefusal(t *testing.T) {
	path := seededNselfConfig(t)
	const broken = "[runtime\nprofile = "
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatalf("seed broken config: %v", err)
	}
	res, err := nselfApplierUnderTest.ApplyDiff(context.Background(), "cascade-nself", []nselfplugin.ConfigEntry{
		{Path: "plugins.cascade-nself.project_dir", Literal: `"/p"`},
	})
	if err == nil {
		t.Fatalf("ApplyDiff on a broken config succeeded: %+v", res)
	}
	const want = "runtime: config: edited config would be malformed TOML: toml: expected ']' to close table name"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
	if len(res.Applied)+len(res.Unchanged)+len(res.Skipped) != 0 {
		t.Fatalf("result = %+v, want the zero result on error", res)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil || string(data) != broken {
		t.Fatalf("config.toml changed by a refused diff (%v):\n%s", rerr, data)
	}
}
