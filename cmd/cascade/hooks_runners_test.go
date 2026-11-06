//go:build !windows

// Purpose: the hook runners against the real cascade-github plugin, installed
// by an attested plugin.add and reached via LaunchInstalled.
// SPORT: cmd/cascade hook runners (TEST, P1-SUP-02).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

const (
	hookWitnessVerb   = "hooks.test.witness"
	hookTestPlugin    = "cascade-github"
	hookChildStoreEnv = "CASCADE_EFFECT_CHILD"
	hookChildExeEnv   = "CASCADE_HOOK_EXE_DIR"
)

var _ = rpc.DeclareAccess(hookWitnessVerb, rpc.AccessOwnerView)

func init() { rpc.RegisterElevatedVerb(hookWitnessVerb, rpc.ElevationRule{Always: true}) }

func hookMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func openHookLog(t *testing.T, path string) (*sqlite.Driver, *audit.EffectLog) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), path)
	hookMust(t, err)
	log, err := audit.NewEffectLog(audit.New(store, runtime.NewSystemClock(), nil))
	hookMust(t, err)
	return store, log
}

type hookEffectSpy struct {
	audit.EffectWriter
	seen    map[string]int
	callCtx context.Context
	onCall  func()
}

func (s *hookEffectSpy) BeginEffect(ctx context.Context, req audit.EffectRequest) (audit.EffectHandle, error) {
	s.seen[req.Action]++
	if req.Action == "plugin.call" {
		s.callCtx = ctx
		s.onCall()
	}
	return s.EffectWriter.BeginEffect(ctx, req)
}

func useHookLaunchEnv(t *testing.T, store provider.Store, log *audit.EffectLog) *hookEffectSpy {
	t.Helper()
	_, err := plugins.RecoverPluginEffects(context.Background(), log, filepath.Join(t.TempDir(), "effects"))
	hookMust(t, err)
	spy := &hookEffectSpy{EffectWriter: log, seen: map[string]int{}, onCall: func() {}}
	t.Cleanup(plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) {
		return plugins.LaunchEnv{Store: store, Effects: spy, Registrar: noopPluginRegistrar{}, Getenv: func(string) string { return "" }}, nil
	}))
	return spy
}

type hookFixture struct {
	daemon elevatedDaemon
	log    *audit.EffectLog
	spy    *hookEffectSpy
	run    hooks.ActionRunner
	exeDir string
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	exeDir := t.TempDir()
	goBuild(t, "../../plugins/github", exeDir, hookTestPlugin)
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	dataDir := filepath.Join(t.TempDir(), "data")
	paths := elevatedTestPaths{fakeDaemonPaths: fakeDaemonPaths{root: filepath.Dir(dataDir)}, data: dataDir}
	resetProcessFirewall(t)
	d := newElevatedDaemon(t, dataDir, true)
	clock := runtime.NewSystemClock()
	log, err := openEffectLog(d.Store, clock, nil, paths)
	hookMust(t, err)
	spy := useHookLaunchEnv(t, d.Store, log)
	manifest, err := os.ReadFile("../../plugins/github/manifest.toml")
	hookMust(t, err)
	attestedAdd(t, d.Call, hookTestPlugin, string(manifest))
	runners, err := hookRunnersFor(&daemonWiring{Store: d.Store, Clock: clock, Paths: paths})
	hookMust(t, err)
	return &hookFixture{d, log, spy, runners[hooks.ActionTypePluginCall], exeDir}
}

func hookTestFire(seq uint64) hooks.Fire {
	return hooks.Fire{Hook: hooks.HookConfig{ID: "h1", ActionType: hooks.ActionTypePluginCall}, Namespace: "ci", EventSeq: seq}
}

var hookTestParams = map[string]string{hooks.PluginParamName: hookTestPlugin, hooks.PluginToolParam: "cascade.hello",
	hookInputParam: `{"min_protocol_version":"1.0.0"}`}

func assertHookPhase(t *testing.T, log *audit.EffectLog, fire hooks.Fire, want audit.EffectPhase) {
	t.Helper()
	got, found, err := log.EffectState(context.Background(), hookEffectKey(fire))
	if err != nil || !found || got != want {
		t.Fatalf("EffectState = %q, %v, %v; want %q", got, found, err, want)
	}
}

func TestHookPluginCallWritesIntentAndConfirm(t *testing.T) {
	f, fire := newHookFixture(t), hookTestFire(1)
	f.spy.onCall = func() { assertHookPhase(t, f.log, fire, audit.EffectIntent) } // the intent precedes the call
	hookMust(t, f.run.RunAction(context.Background(), fire, hookTestParams))
	assertHookPhase(t, f.log, fire, audit.EffectConfirmed)
	if f.spy.seen["plugin.launch"] != 1 || f.spy.seen["plugin.call"] != 1 {
		t.Fatalf("effects seen = %v; want one launch and one call", f.spy.seen)
	}
}

func TestHookPluginCallDuplicateDeliveryRunsOnce(t *testing.T) {
	f, fire, ctx := newHookFixture(t), hookTestFire(2), context.Background()
	hookMust(t, f.run.RunAction(ctx, fire, hookTestParams))
	err := f.run.RunAction(ctx, fire, hookTestParams)
	if !errors.Is(err, errHookDuplicateDelivery) || !strings.Contains(err.Error(), "the plugin call was not repeated") {
		t.Fatalf("redelivery = %v; want errHookDuplicateDelivery", err)
	}
	if f.spy.seen["plugin.call"] != 1 {
		t.Fatalf("plugin calls = %d, want 1", f.spy.seen["plugin.call"])
	}
}

func TestPluginCallHookRefusesUntrustedPlugin(t *testing.T) {
	f, ctx := newHookFixture(t), context.Background()
	base, _, err := plugins.LoadMetadata(ctx, f.daemon.Store, hookTestPlugin)
	hookMust(t, err)
	untrusted, disabled := base, base
	untrusted.TrustTier, disabled.Enabled = "", false
	for i, rec := range []plugins.PluginMetadata{untrusted, disabled} {
		hookMust(t, plugins.SaveMetadata(ctx, f.daemon.Store, rec))
		fire := hookTestFire(uint64(10 + i))
		if err := f.run.RunAction(ctx, fire, hookTestParams); !cascade.HasKind(err, cascade.KindPolicyDenied) {
			t.Fatalf("case %d: RunAction = %v; want KindPolicyDenied", i, err)
		}
		assertHookPhase(t, f.log, fire, audit.EffectFailed)
		if len(f.spy.seen) != 0 {
			t.Fatalf("case %d: effects seen = %v; want none", i, f.spy.seen)
		}
	}
}

func TestHookPluginCallCarriesToolOrigin(t *testing.T) {
	f, carried, out := newHookFixture(t), false, any(nil)
	f.daemon.Registry.Register(hookWitnessVerb, func(ctx context.Context, _ json.RawMessage) (any, error) {
		_, carried = rpc.VerifiedElevationFrom(ctx)
		return nil, f.run.RunAction(ctx, hookTestFire(3), hookTestParams)
	})
	hookMust(t, f.daemon.Call(context.Background(), hookWitnessVerb, map[string]string{}, &out))
	if !carried {
		t.Fatal("the Run ctx held no witness; the test proves nothing")
	}
	if o, ok := rpc.OriginFrom(f.spy.callCtx); !ok || o != rpc.OriginTool {
		t.Fatalf("OriginFrom at the plugin call = %q, %v; want OriginTool", o, ok)
	}
	if _, ok := rpc.VerifiedElevationFrom(f.spy.callCtx); ok {
		t.Fatal("a witness reached the plugin call")
	}
}

// exitOnConfirm is the crash point: after the call, before its record.
type exitOnConfirm struct{ audit.EffectWriter }

func (exitOnConfirm) ConfirmEffect(context.Context, audit.EffectHandle, string) (audit.Record, error) {
	os.Exit(3)
	return audit.Record{}, nil
}

func TestHookPluginCallCrashLeavesUnknownOutcome(t *testing.T) {
	ctx, fire := context.Background(), hookTestFire(4)
	if path := os.Getenv(hookChildStoreEnv); path != "" { // the SEC-33 child: begin, call, exit 3
		store, log := openHookLog(t, path)
		t.Cleanup(plugins.SetExecutableDirForTest(os.Getenv(hookChildExeEnv)))
		useHookLaunchEnv(t, store, log).onCall = func() { hookMust(t, os.WriteFile(path+".calls", []byte("x"), 0o600)) }
		r := hookPluginRunner{effects: exitOnConfirm{log}, launch: plugins.InstalledHandleProvider()}
		t.Fatalf("the child survived its crash point: %v", r.RunAction(ctx, fire, hookTestParams))
	}
	f := newHookFixture(t)
	rec, _, err := plugins.LoadMetadata(ctx, f.daemon.Store, hookTestPlugin)
	hookMust(t, err)
	path := filepath.Join(t.TempDir(), "cascade.db")
	seed, _ := openHookLog(t, path)
	hookMust(t, plugins.SaveMetadata(ctx, seed, rec))
	hookMust(t, seed.Close())
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1") //nolint:gosec // this test binary
	child.Env = append(os.Environ(), hookChildStoreEnv+"="+path, hookChildExeEnv+"="+f.exeDir)
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("child = %v; want exit 3\n%s", err, out)
	}
	store, log := openHookLog(t, path)
	defer func() { _ = store.Close() }()
	assertHookPhase(t, log, fire, audit.EffectIntent)
	_, err = log.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: "o", Action: "o", Key: "other:1"})
	hookMust(t, err)
	if n, err := recoverHookEffects(ctx, log); err != nil || n != 1 { // other:1 stays open for its own site
		t.Fatalf("recoverHookEffects = %d, %v; want 1", n, err)
	}
	assertHookPhase(t, log, fire, audit.EffectUnknown)
	if calls, err := os.ReadFile(path + ".calls"); err != nil || len(calls) != 1 {
		t.Fatalf("calls = %q, %v; want exactly one", calls, err)
	}
}

func noteRunner(t *testing.T) (hooks.ActionRunner, *memory.FileStore, string) {
	t.Helper()
	root := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, root)
	}
	paths, clock := fakeDaemonPaths{root: root}, runtime.NewSystemClock()
	resetProcessFirewall(t)
	runners, err := hookRunnersFor(&daemonWiring{Store: storetest.NewMemStore(), Clock: clock, Paths: paths})
	hookMust(t, err)
	return runners[hooks.ActionTypeAgentNote], memory.NewFileStore(memoryStoreDir(paths), clock), memoryStoreDir(paths)
}

var noteFire = hooks.Fire{Hook: hooks.HookConfig{ID: "n1", ActionType: hooks.ActionTypeAgentNote}, Namespace: "ci", EventSeq: 1}

func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*", "*"))
	hookMust(t, err)
	var all strings.Builder
	for _, p := range files {
		b, err := os.ReadFile(p)
		hookMust(t, err)
		all.WriteString(p + "\x00" + string(b) + "\x00")
	}
	return all.String()
}

func TestNoteHookWritesMemoryWithHookProvenance(t *testing.T) {
	run, store, _ := noteRunner(t)
	note := map[string]string{hooks.NoteNameParam: "deploy-note", hookNoteBodyParam: "the deploy ran"}
	hookMust(t, run.RunAction(context.Background(), noteFire, note))
	got, err := store.Read(context.Background(), memory.KindProject, "deploy-note")
	hookMust(t, err)
	if got.Provenance.SessionID != "hook:n1" || got.Provenance.Origin != memory.OriginHarness || got.Body != "the deploy ran" {
		t.Fatalf("stored note = %+v", got)
	}
}

func TestNoteHookRefusesSecret(t *testing.T) {
	run, _, dir := noteRunner(t)
	hookMust(t, run.RunAction(context.Background(), noteFire, map[string]string{hooks.NoteNameParam: "kept", hookNoteBodyParam: "plain text"}))
	before := snapshotDir(t, dir)
	shaped := "ghp_" + strings.Repeat("a1B2", 9) // assembled here: a shape, not a credential
	err := run.RunAction(context.Background(), noteFire, map[string]string{hooks.NoteNameParam: "leak", hookNoteBodyParam: "token " + shaped})
	if !errors.Is(err, errHookNoteSecret) || !strings.Contains(err.Error(), "secret material") || strings.Contains(err.Error(), shaped) {
		t.Fatalf("RunAction = %v; want errHookNoteSecret", err)
	}
	if before == "" || snapshotDir(t, dir) != before {
		t.Fatal("the memory dir changed")
	}
}

func TestRegisterHookRunnerRejectsDuplicateAndShell(t *testing.T) {
	for _, typ := range []hooks.ActionType{hooks.ActionTypePluginCall, hooks.ActionTypeShell} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("registering %s did not panic", typ)
				}
			}()
			registerHookRunner(hookRunnerRegistration{Type: typ, Build: buildAgentNoteRunner})
		}()
	}
	if !hookRunnable(hooks.ActionTypePluginCall) || !hookRunnable(hooks.ActionTypeAgentNote) || hookRunnable(hooks.ActionTypeShell) {
		t.Fatal("hookRunnable disagrees with the registrations")
	}
}
