//go:build !windows

// Purpose: hooks scenarios on the composed daemon: load set, shell refusal,
// reload, Router deny, redaction sweeps and the doctor census.
// Inputs: composeElevated over temp homes, the real cascade-github plugin,
// an injected in-memory custody, config.accept_loosening.
// SPORT: cmd/cascade hooks composed-daemon scenarios (TEST).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
)

const (
	echoVaultName = "ECHO_KEY"
	echoCanary    = "cnry-5e1d9a7b3c2f8064"
	echoTag       = "<apikey>" + echoVaultName + "</apikey>"
	echoInput     = `{"args":["get","` + echoTag + `"]}`
	helloInput    = `{"min_protocol_version":"1.0.0","note":"` + echoTag + `"}`
	effectHookTop = "namespace = \"hooktest\"\ntrigger = \"hooktest.go\"\n"
	// echoHookBody fails: the plugin's repos verb quotes the bad target back.
	echoHookBody = "[[hooks]]\nid = \"echo\"\n" + effectHookTop + "action_type = \"plugin-call\"\n" +
		"action_params = { plugin = \"cascade-github\", tool = \"cascade-github.cmd.repos\", input = '" + echoInput + "' }\n"
	helloHookBody = "[[hooks]]\nid = \"tagged-hello\"\n" + effectHookTop + "action_type = \"plugin-call\"\n" +
		"action_params = { plugin = \"cascade-github\", tool = \"cascade.hello\", input = '" + helloInput + "' }\n"
	shellHookBody = "[[hooks]]\nid = \"sh\"\n" + effectHookTop + "action_type = \"shell\"\naction_params = { command = \"true\" }\n"
)

// effectCustody is the process vault, counting reads of the echo name.
type effectCustody struct {
	*hookCountingCustody
	echoReads atomic.Int64
}

func (c *effectCustody) Get(ctx context.Context, name string) ([]byte, error) {
	if name == echoVaultName {
		c.echoReads.Add(1)
	}
	return c.hookCountingCustody.Get(ctx, name)
}

// effectRig is a composed daemon with the plugin installed, the counting
// vault and a spy on the launch env's effects. The spy's map is written
// before a fire is recorded and read after hooks.fires shows it.
type effectRig struct {
	c     composedElevated
	w     *daemonWiring
	vault *effectCustody
	spy   *hookEffectSpy
}

func newEffectRig(t *testing.T) *effectRig {
	t.Helper()
	exeDir := t.TempDir()
	goBuild(t, "../../plugins/github", exeDir, hookTestPlugin)
	t.Cleanup(plugins.SetExecutableDirForTest(exeDir))
	resetProcessFirewall(t)
	vault := &effectCustody{hookCountingCustody: &hookCountingCustody{items: map[string][]byte{echoVaultName: []byte(echoCanary)}}}
	selectProcessCustody = func(secrets.Config) (secrets.Custody, error) { return vault, nil }
	wiring := captureWiring(t)
	r := &effectRig{c: composeElevated(t, true), vault: vault}
	r.w = *wiring
	manifest, err := os.ReadFile("../../plugins/github/manifest.toml")
	hookMust(t, err)
	attestedAdd(t, r.c.Call, hookTestPlugin, string(manifest))
	env := *pluginLaunchWired.Load()
	r.spy = &hookEffectSpy{EffectWriter: env.Effects, seen: map[string]int{}, onCall: func() {}}
	env.Effects = r.spy
	t.Cleanup(plugins.SetLaunchEnvSource(func(context.Context) (plugins.LaunchEnv, error) { return env, nil }))
	return r
}

// accept writes body and admits it through config.accept_loosening.
func (r *effectRig) accept(t *testing.T, body string) {
	t.Helper()
	writeConfig(t, r.c.Deps.Paths.ConfigPath(), body)
	var out acceptLooseningResult
	hookMust(t, r.c.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(body), &out))
	if !out.Reload.Accepted {
		t.Fatalf("config.accept_loosening did not apply the edit: %+v", out)
	}
}

// awaitHook waits until hooks.list names exactly id.
func (r *effectRig) awaitHook(t *testing.T, id string) hooksListResult {
	t.Helper()
	var listed hooksListResult
	hookWaitFor(t, "hook "+id, func() bool {
		return callOwner(t, r.c.Registry, hooksListMethod, &listed) == nil && len(listed.Hooks) == 1 && listed.Hooks[0].ID == id
	})
	return listed
}

// fire publishes the trigger and returns the fires ring once it holds n.
func (r *effectRig) fire(t *testing.T, n int) hooksFiresResult {
	t.Helper()
	_, err := r.w.Bus.Publish(context.Background(), fireTriggerNS, fireTriggerKind, "test", []byte(`{}`))
	hookMust(t, err)
	var fires hooksFiresResult
	hookWaitFor(t, "fire records", func() bool {
		return callOwner(t, r.c.Registry, hooksFiresMethod, &fires) == nil && len(fires.Fires) >= n
	})
	return fires
}

// plainNeedles are the rehydrated input, the canary and the input's hash.
func plainNeedles(tagged string) []string {
	plain := strings.ReplaceAll(tagged, echoTag, echoCanary)
	sum := sha256.Sum256([]byte(plain))
	return []string{echoCanary, plain, hex.EncodeToString(sum[:])}
}

// assertNoNeedle fails for every surface, or file under the daemon's root or
// CASCADE_HOME, holding a needle. A file removed mid-walk is skipped.
func (r *effectRig) assertNoNeedle(t *testing.T, needles []string, surfaces map[string][]byte) {
	t.Helper()
	for _, root := range []string{filepath.Dir(r.c.Deps.Paths.ConfigPath()), os.Getenv("CASCADE_HOME")} {
		hookMust(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				surfaces[p], err = os.ReadFile(p)
			}
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}))
	}
	for where, b := range surfaces {
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				t.Errorf("%s holds %q", where, n)
			}
		}
	}
}

func TestHookRunnerRegistrationsMatchLoadSet(t *testing.T) {
	const probe hooks.ActionType = "probe-runner"
	hookRunnerTable[probe] = hookRunnerRegistration{Type: probe, Build: buildAgentNoteRunner}
	t.Cleanup(func() { delete(hookRunnerTable, probe) })
	for at := range hookRunnerTable {
		if !hookLoadable(at) {
			t.Errorf("registered runner type %q is not in the load set", at)
		}
	}
	for _, at := range []hooks.ActionType{hooks.ActionTypePluginCall, hooks.ActionTypeAgentNote, hooks.ActionTypeShell, probe, "unregistered"} {
		if hookLoadable(at) != hookRunnable(at) || (at == hooks.ActionTypeShell && hookLoadable(at)) {
			t.Errorf("hookLoadable(%q) = %v; the registrations say %v", at, hookLoadable(at), hookRunnable(at))
		}
	}
}

func TestShellHookRefusedAtLoad(t *testing.T) {
	deps := freshDaemonHome(t)
	wiring := captureWiring(t)
	reg, _ := composeBaselineDaemon(t, deps, shellHookBody)
	var listed hooksListResult
	if eo := callOwner(t, reg, hooksListMethod, &listed); eo != nil || len(listed.Hooks) != 0 {
		t.Fatalf("hooks.list = %+v, %+v; want an empty list", listed, eo)
	}
	named := false
	for _, s := range (*wiring).Manifest.Snapshot() {
		named = named || (s.State == daemon.SubsystemError && strings.Contains(s.Detail, hooks.SectionName))
	}
	if !named {
		t.Fatalf("no manifest entry names the refused [hooks] section: %+v", (*wiring).Manifest.Snapshot())
	}
}

func TestHooksReloadThroughBus(t *testing.T) {
	r := newEffectRig(t)
	r.accept(t, fireHookBody)
	r.awaitHook(t, "greet")
	if f := r.fire(t, 1).Fires[0]; f.HookID != "greet" || f.ResultCode != hooks.ResultSuccess {
		t.Fatalf("accepted hook fire = %+v; want greet/success", f)
	}
	obs, err := r.w.Bus.SubscribeFromHead(context.Background(), hotReloadEventNamespace, "hooks-effect-observer", 64)
	hookMust(t, err)
	defer func() { _ = obs.Unsubscribe() }()
	r.accept(t, fireHookBody+shellHookBody)
	rejected := false
	hookWaitFor(t, "hooks.reload.rejected", func() bool {
		select {
		case ev := <-obs.Events:
			rejected = rejected || ev.Kind == hooks.EventKindReloadRejected
		default:
		}
		return rejected
	})
	r.awaitHook(t, "greet")
	fires := r.fire(t, 2).Fires
	if last := fires[len(fires)-1]; last.HookID != "greet" || last.ResultCode != hooks.ResultSuccess {
		t.Fatalf("fire after the rejected reload = %+v; want the previous set firing", last)
	}
}

func TestHookRouterDenyBlocksPluginCall(t *testing.T) {
	r := newEffectRig(t)
	hookMust(t, r.w.Deps.Policy.Grants.Grant(context.Background(), policy.Grant{Subject: schedulerSubject(),
		Capability: hooksPluginCallCap, ScopeClass: corpus.VisibilityTeam, Verdict: policy.VerdictDeny}))
	r.accept(t, helloHookBody)
	r.awaitHook(t, "tagged-hello")
	if f := r.fire(t, 1).Fires[0]; f.ResultCode != hooks.ResultRefused {
		t.Fatalf("denied fire = %+v; want refused", f)
	}
	if n, v := r.spy.seen["plugin.launch"], r.vault.echoReads.Load(); n != 0 || v != 0 {
		t.Fatalf("a denied hook launched the plugin %d times and read the vault %d times; want 0 and 0", n, v)
	}
}

func TestFailingFireEchoRedactedEverywhere(t *testing.T) {
	r := newEffectRig(t)
	r.accept(t, echoHookBody)
	listed := r.awaitHook(t, "echo")
	fires := r.fire(t, 1)
	f := fires.Fires[0]
	if f.ResultCode != hooks.ResultError || !strings.Contains(f.ErrMsg, "needs <owner>/<repo>") || f.ParamsHash == "" {
		t.Fatalf("echo fire = %+v; want a failing fire carrying the plugin's echo and a params hash", f)
	}
	if r.vault.echoReads.Load() == 0 || r.spy.seen["plugin.launch"] == 0 {
		t.Fatal("the echo fire never rehydrated or launched; the sweep would pass vacuously")
	}
	r.assertNoNeedle(t, plainNeedles(echoInput), map[string][]byte{"hooks.list and hooks.fires": fmt.Appendf(nil, "%+v %+v", listed, fires)})
}

func TestRehydratedCallLeavesNoPlaintextOrHash(t *testing.T) {
	r := newEffectRig(t)
	r.accept(t, helloHookBody)
	r.awaitHook(t, "tagged-hello")
	if f := r.fire(t, 1).Fires[0]; f.ResultCode != hooks.ResultSuccess {
		t.Fatalf("rehydrated fire = %+v; want success", f)
	}
	if r.vault.echoReads.Load() == 0 {
		t.Fatal("the fire never read the vault; the sweep would pass vacuously")
	}
	r.assertNoNeedle(t, plainNeedles(helloInput), map[string][]byte{})
}

// hooksCensus runs the doctor census over status.get's hooks entry alone.
func hooksCensus(t *testing.T, reg *rpc.Registry) doctor.CheckResult {
	t.Helper()
	var resp daemon.StatusResponse
	if eo := callOwner(t, reg, "status.get", &resp); eo != nil {
		t.Fatalf("status.get: %+v", eo)
	}
	only := slices.DeleteFunc(resp.Subsystems, func(s daemon.SubsystemStatus) bool { return s.Name != hooksSubsystem })
	return runCheck(t, censusFor(statusOf(daemon.StatusResponse{Subsystems: only})))
}

func TestHooksDegradedVisibleInDoctorCensus(t *testing.T) {
	t.Run("started", func(t *testing.T) {
		if res := hooksCensus(t, composeForTest(t).Registry); res.Status != doctor.StatusOK || !strings.HasPrefix(res.Message, "1/1 ") {
			t.Fatalf("census over a started hooks subsystem = %+v; want ok", res)
		}
	})
	t.Run("failed", func(t *testing.T) {
		scratchRegistrations(t)
		daemonRegistrations = append([]daemonRegistration(nil), daemonRegistrations...)
		for i := range daemonRegistrations {
			if daemonRegistrations[i].Name == hooksSubsystem {
				daemonRegistrations[i].Wire = func(w *daemonWiring) error {
					return wireHooksWith(w, func(*daemonWiring) (*hooksParts, error) { return nil, errors.New("injected build failure") })
				}
			}
		}
		res := hooksCensus(t, composeForTest(t).Registry)
		if res.Status != doctor.StatusError || !strings.Contains(res.Detail+res.Message, hooksSubsystem) {
			t.Fatalf("census over a failed hooks subsystem = %+v; want an error naming hooks", res)
		}
	})
}
