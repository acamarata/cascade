//go:build !windows

// Purpose: the generic process-plugin verb reaches its plugin only through a
// live, trusted handle: the real cascade-github answers a refused argument
// with a typed error, an untrusted, disabled or missing plugin never
// launches, a plugin error keeps its Kind, the child is reaped when the
// command returns, and a dispatched result is labelled untrusted-source.
// Constraints: no test reaches api.github.com. The real executable is only
// given an argument it refuses before egress; success rendering uses the
// lifecycle test plugin, which answers any method.
// SPORT: cmd/cascade:plugin-process-dispatch (TESTED) — P1-PLG-04.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// verbFix is one trusted-plugin test environment.
type verbFix struct {
	d        elevatedDaemon
	log      *audit.Log
	launches *atomic.Int32
	last     *atomic.Pointer[plugins.TrustedHandle]
}

// newVerbFix installs a launch environment over a fresh elevated daemon
// store with exeDir as the executable directory, and counts successful
// launches made through the dispatch seam.
func newVerbFix(t *testing.T, exeDir string) verbFix {
	t.Helper()
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	t.Setenv("CASCADE_SOCKET", filepath.Join(t.TempDir(), "daemon.sock"))
	log := audit.New(d.Store, runtime.NewSystemClock(), nil)
	el, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.RecoverPluginEffects(context.Background(), el, filepath.Join(t.TempDir(), "plugin-effects")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) {
		return plugins.LaunchEnv{Store: d.Store, Effects: el, Registrar: noopPluginRegistrar{}, Getenv: func(string) string { return "" }}, nil
	}))
	launches, last := &atomic.Int32{}, &atomic.Pointer[plugins.TrustedHandle]{}
	prev := processVerbLauncher
	processVerbLauncher = func(ctx context.Context, id string) (*plugins.TrustedHandle, error) {
		h, err := plugins.LaunchInstalled(ctx, id)
		if err == nil {
			launches.Add(1)
			last.Store(h)
		}
		return h, err
	}
	t.Cleanup(func() { processVerbLauncher = prev })
	return verbFix{d: d, log: log, launches: launches, last: last}
}

// githubFix builds the real cascade-github and, when trust is set, installs
// it through an attested plugin.add.
func githubFix(t *testing.T, trust bool) verbFix {
	t.Helper()
	exeDir := t.TempDir()
	goBuild(t, "../../plugins/github", exeDir, "cascade-github")
	f := newVerbFix(t, exeDir)
	if trust {
		manifest, err := os.ReadFile(filepath.Join("..", "..", "plugins", "github", "manifest.toml"))
		if err != nil {
			t.Fatal(err)
		}
		attestedAdd(t, f.d.Call, "cascade-github", string(manifest))
	}
	return f
}

// runVerb executes the real root tree and returns its combined output.
func runVerb(t *testing.T, args ...string) (string, error) {
	t.Helper()
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// callRecords counts the plugin.call effect records by phase.
func (f verbFix) callRecords(t *testing.T) map[string]int {
	t.Helper()
	got := map[string]int{}
	filter := audit.Filter{}
	for {
		page, err := f.log.Query(context.Background(), filter)
		if err != nil {
			t.Fatalf("audit query: %v", err)
		}
		for _, r := range page.Records {
			if strings.HasPrefix(r.EffectKey, "plugin.call:") {
				got[r.EffectPhase]++
			}
		}
		if page.NextCursor == "" {
			return got
		}
		filter.Cursor = page.NextCursor
	}
}

// rendered returns what the output writer prints for err.
func rendered(err error, jsonMode bool) string {
	var buf bytes.Buffer
	output.New(&buf, &buf, jsonMode, false, false, true).Fail(err)
	return buf.String()
}

// refusedArgs is an owner the plugin refuses before any egress (the path-segment guard
// rejects "..", so no request is ever built).
var refusedArgs = []string{"github", "repos", "get", "a..b/repo"}

func TestProcessVerbDispatchesToTrustedHandle(t *testing.T) {
	f := githubFix(t, true)
	_, err := runVerb(t, refusedArgs...)
	if err == nil || !cascade.HasKind(err, cascade.KindInternal) || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("err = %v, want the plugin's typed refusal naming the owner", err)
	}
	if f.launches.Load() != 1 {
		t.Fatalf("launches = %d, want 1", f.launches.Load())
	}
	if human := rendered(err, false); !strings.Contains(human, "owner") {
		t.Errorf("human rendering = %q, want the plugin refusal", human)
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(rendered(err, true)), &env); jerr != nil || env.OK || env.Error.Kind != "internal" ||
		!strings.Contains(env.Error.Message, "owner") {
		t.Fatalf("json rendering = %+v (%v), want a failed envelope of kind internal naming the owner", env, jerr)
	}
	if got := f.callRecords(t); got["intent"] != 1 || got["failed"] != 1 || len(got) != 2 {
		t.Fatalf("call records = %v, want one intent and one failed", got)
	}
}

func TestProcessVerbRefusesUntrusted(t *testing.T) {
	for _, state := range []string{"missing", "untrusted", "disabled"} {
		t.Run(state, func(t *testing.T) {
			f := githubFix(t, state != "missing")
			ctx := context.Background()
			switch state {
			case "untrusted":
				rec, ok, err := plugins.LoadMetadata(ctx, f.d.Store, "cascade-github")
				if err != nil || !ok {
					t.Fatalf("LoadMetadata = %v, %v", ok, err)
				}
				rec.TrustTier = ""
				if err := plugins.SaveMetadata(ctx, f.d.Store, rec); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if _, err := plugins.SetEnabled(ctx, f.d.Store, "cascade-github", false); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runVerb(t, refusedArgs...)
			if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "`cascade plugin add cascade-github`") {
				t.Fatalf("err = %v, want KindUnavailable naming `cascade plugin add cascade-github`", err)
			}
			if f.launches.Load() != 0 || strings.Contains(out, "untrusted plugin output") {
				t.Fatalf("launches = %d, output = %q; a refused verb must start nothing", f.launches.Load(), out)
			}
		})
	}
}

func TestProcessVerbPluginErrorIsTyped(t *testing.T) {
	githubFix(t, true)
	out, err := runVerb(t, append(refusedArgs, "--json")...)
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInternal || !strings.Contains(err.Error(), "plugin returned error") {
		t.Fatalf("err = %v (kind %v, typed %v), want the plugin's error reply with its Kind", err, kind, ok)
	}
	if strings.Contains(out, "provenance") || strings.Contains(out, "untrusted plugin output") {
		t.Fatalf("a plugin error wrote success output: %q", out)
	}
}

// reapFix trusts the lifecycle plugin as reapdemo and mounts one verb that
// calls cascade.hello on it.
func reapFix(t *testing.T) (verbFix, string) {
	t.Helper()
	exeDir, out := t.TempDir(), t.TempDir()
	installReapDemo(t, exeDir, out)
	f := newVerbFix(t, exeDir)
	attestedAdd(t, f.d.Call, "reapdemo", reapManifest)
	prev := processPluginSpecs
	processPluginSpecs = func() []processPluginCommand {
		return []processPluginCommand{{PluginID: "reapdemo", Spec: plugin.CommandSpec{
			Name: "reap-run", Description: "reap demo", RPCMethod: "cascade.hello",
		}}}
	}
	t.Cleanup(func() { processPluginSpecs = prev })
	return f, out
}

func TestProcessVerbClosesHandle(t *testing.T) {
	f, out := reapFix(t)
	if _, err := runVerb(t, "reap", "run"); err != nil {
		t.Fatalf("reap run: %v", err)
	}
	// The handle the verb launched refuses further calls: it was closed.
	if _, err := f.last.Load().Call(context.Background(), "cascade.hello", nil); !cascade.HasKind(err, cascade.KindUnavailable) ||
		!strings.Contains(err.Error(), "after Close") {
		t.Fatalf("Call on the verb's handle = %v, want the after-Close refusal", err)
	}
	waitGone(t, waitPid(t, out, "env-*")) // reaped before the command returned
}

func TestProcessVerbOutputMarkedUntrusted(t *testing.T) {
	reapFix(t)
	human, err := runVerb(t, "reap", "run")
	if err != nil || !strings.Contains(human, "untrusted plugin output") {
		t.Fatalf("human output = %q (%v), want the untrusted header", human, err)
	}
	out, err := runVerb(t, "reap", "run", "--json")
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Provenance string          `json:"provenance"`
			Plugin     string          `json:"plugin"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal([]byte(out), &env) != nil || !env.OK ||
		env.Data.Provenance != "untrusted-source" || env.Data.Plugin != "reapdemo" || !strings.Contains(string(env.Data.Result), "protocol_version") {
		t.Fatalf("json output = %q (%v), want an ok envelope with provenance untrusted-source and the plugin result", out, err)
	}
}

// TestProcessVerbEnvelopeCarriesArgsAndFlags pins contract:process-verb-envelope:
// positionals in order, only changed flags, values as strings.
func TestProcessVerbEnvelopeCarriesArgsAndFlags(t *testing.T) {
	var got []byte
	cmd := processLeafCmd("repos", processPluginCommand{PluginID: "x", Spec: plugin.CommandSpec{Name: "x-repos", RPCMethod: "x.cmd"}}, nil)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		var err error
		got, err = processVerbEnvelope(c, args)
		return err
	}
	cmd.SetArgs([]string{"list", "acamarata", "--per-page", "2", "--state", "open"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := `{"args":["list","acamarata"],"flags":{"per-page":"2","state":"open"}}`; string(got) != want {
		t.Fatalf("envelope = %s, want %s", got, want)
	}
}
