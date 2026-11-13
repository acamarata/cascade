//go:build !windows

// Purpose: the hooks composition at the real composition root: a configured
// plugin-call hook fires through the composed daemon, the registration
// mounts the verbs, a build error degrades without stopping the daemon, and
// an unaccepted hook edit is not applied and raises one attention item.
// Inputs: composeDaemon over temp homes, the real cascade-github plugin as
// the trusted fixture, config.accept_loosening over the elevated caller.
// SPORT: cmd/cascade hooks composition tests (P1-SUP-02).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	fireTriggerNS   = "hooktest"
	fireTriggerKind = "hooktest.go"
	fireHookBody    = "[[hooks]]\nid = \"greet\"\nnamespace = \"hooktest\"\ntrigger = \"hooktest.go\"\naction_type = \"plugin-call\"\n" +
		"action_params = { plugin = \"cascade-github\", tool = \"cascade.hello\", input = '{\"min_protocol_version\":\"1.0.0\"}' }\n"
)

// captureWiring registers a late scratch step that keeps the composed
// daemonWiring (bus, store, clock) for the test, and restores the lists.
func captureWiring(t *testing.T) **daemonWiring {
	t.Helper()
	scratchRegistrations(t)
	var got *daemonWiring
	_ = registerDaemonWiring(daemonRegistration{Name: "scratch-capture-wiring", Phase: phaseLate, Order: 9998,
		Wire: func(w *daemonWiring) error { got = w; return nil }})
	return &got
}

func hookOwnerCtx() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

// callOwner dispatches method as the owner and decodes the reply into out.
func callOwner(t *testing.T, reg *rpc.Registry, method string, out any) *rpc.ErrorObject {
	t.Helper()
	res, eo := reg.Dispatch(hookOwnerCtx(), &rpc.Request{JSONRPC: "2.0", Method: method, Params: json.RawMessage(`{}`)})
	if eo != nil || out == nil {
		return eo
	}
	raw, err := json.Marshal(res)
	if err == nil {
		err = json.Unmarshal(raw, out)
	}
	if err != nil {
		t.Fatalf("%s: decode: %v", method, err)
	}
	return nil
}

// hookWaitFor polls cond for up to 30 seconds.
func hookWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func hookSubsystemState(t *testing.T, m *daemon.Manifest, name string) daemon.SubsystemState {
	t.Helper()
	for _, s := range m.Snapshot() {
		if s.Name == name {
			return s.State
		}
	}
	t.Fatalf("the manifest has no %q entry", name)
	return 0
}

func TestDaemonFiresConfiguredPluginCallHook(t *testing.T) {
	isolateHome(t)
	exeDir := t.TempDir()
	goBuild(t, "../../plugins/github", exeDir, hookTestPlugin)
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	wiring := captureWiring(t)
	c := composeElevated(t, true)
	manifest, err := os.ReadFile("../../plugins/github/manifest.toml")
	hookMust(t, err)
	attestedAdd(t, c.Call, hookTestPlugin, string(manifest))
	writeConfig(t, c.Deps.Paths.ConfigPath(), fireHookBody)
	var accepted acceptLooseningResult
	hookMust(t, c.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(fireHookBody), &accepted))
	if !accepted.Reload.Accepted {
		t.Fatalf("config.accept_loosening did not apply the hook: %+v", accepted)
	}
	var listed hooksListResult
	hookWaitFor(t, "the accepted hook to be registered", func() bool {
		return callOwner(t, c.Registry, hooksListMethod, &listed) == nil && len(listed.Hooks) == 1
	})
	w := *wiring
	if _, err := w.Bus.Publish(context.Background(), fireTriggerNS, fireTriggerKind, "test", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var fires hooksFiresResult
	hookWaitFor(t, "a fire record", func() bool {
		return callOwner(t, c.Registry, hooksFiresMethod, &fires) == nil && len(fires.Fires) > 0
	})
	got := fires.Fires[0]
	if got.HookID != "greet" || got.ResultCode != hooks.ResultSuccess || got.ParamsHash != listed.Hooks[0].ParamsHash {
		t.Fatalf("fire = %+v; want greet/success with the configured params hash %s", got, listed.Hooks[0].ParamsHash)
	}
}

func TestHooksMountedThroughRegistration(t *testing.T) {
	c := composeForTest(t)
	if st := hookSubsystemState(t, c.Opts.Manifest, hooksSubsystem); st != daemon.SubsystemRunning {
		t.Fatalf("hooks subsystem state = %v; want running", st)
	}
	var listed hooksListResult
	if eo := callOwner(t, c.Registry, hooksListMethod, &listed); eo != nil || len(listed.Hooks) != 0 {
		t.Fatalf("hooks.list = %+v, %+v; want an empty list", listed, eo)
	}
	if eo := callOwner(t, c.Registry, hooksFiresMethod, nil); eo != nil {
		t.Fatalf("hooks.fires: %+v", eo)
	}
}

func TestHooksBuildErrorRecordedDegraded(t *testing.T) {
	scratchRegistrations(t)
	daemonRegistrations = append([]daemonRegistration(nil), daemonRegistrations...) // the copy keeps the shared array untouched
	for i := range daemonRegistrations {
		if daemonRegistrations[i].Name == hooksSubsystem {
			daemonRegistrations[i].Wire = func(w *daemonWiring) error {
				return wireHooksWith(w, func(*daemonWiring) (*hooksParts, error) { return nil, errors.New("injected build failure") })
			}
		}
	}
	c := composeForTest(t)
	if st := hookSubsystemState(t, c.Opts.Manifest, hooksSubsystem); st != daemon.SubsystemError {
		t.Fatalf("hooks subsystem state = %v; want error", st)
	}
	if st := hookSubsystemState(t, c.Opts.Manifest, "rpc-registry"); st != daemon.SubsystemRunning {
		t.Fatalf("an unrelated subsystem is %v; want running", st)
	}
	for _, m := range []string{hooksListMethod, hooksFiresMethod} {
		eo := callOwner(t, c.Registry, m, nil)
		if eo == nil || !strings.Contains(eo.Message, "hooks subsystem degraded") {
			t.Fatalf("%s: %+v; want hooks subsystem degraded", m, eo)
		}
	}
	if _, err := (hooksRPC{}).list(context.Background(), nil); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("degraded list: %v; want KindUnavailable", err)
	}
}

func TestUnacceptedHookEditNotAppliedRaisesAttention(t *testing.T) {
	deps := freshDaemonHome(t)
	wiring := captureWiring(t)
	for boot := 1; boot <= 2; boot++ { // the second boot is the restart
		reg, closeDaemon := composeBaselineDaemon(t, deps, fireHookBody)
		var listed hooksListResult
		if eo := callOwner(t, reg, hooksListMethod, &listed); eo != nil || len(listed.Hooks) != 0 {
			for _, s := range (*wiring).Manifest.Snapshot() {
				if s.Name == hooksSubsystem {
					t.Logf("hooks entry: %v %s", s.State, s.Detail)
				}
			}
			t.Fatalf("boot %d: hooks.list = %+v, %+v; an unaccepted hook was applied", boot, listed, eo)
		}
		store := supervision.NewStore((*wiring).Store, (*wiring).Clock, nil, supervision.NewSystemIDGenerator(), 0)
		items, err := store.ListInScopes(context.Background(), []supervision.ScopeRef{{Kind: scope.ScopeKindGlobal}}, supervision.Filter{IncludeAcked: true})
		hookMust(t, err)
		n := 0
		for _, it := range items {
			if it.Kind == supervision.KindElevationRefused && strings.HasPrefix(it.SourceRef, "hooks:") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("boot %d: %d hooks elevation-refused items (%+v); want exactly one", boot, n, items)
		}
		closeDaemon()
	}
}
