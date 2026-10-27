//go:build !windows

// Purpose: proves a degraded boot runs values no looser than the config's
// own (autonomy stays strict, [sync] never widens as the sync RPCs see it),
// and that the two tokenless reload paths the runtime has (the fsnotify
// watcher, which production starts, and the SIGHUP handler, which no
// production code registers yet) never use an armed loosening slot.
// Inputs: temp configs, composed daemons, the accept fixture.
// Outputs: none (tests).
// Constraints: no keychain; the SIGHUP leg signals this test process only
// while runtime.RegisterSIGHUP's handler is installed.
// SPORT: cmd/cascade config baseline tests (P1-SEC-04).
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDaemonBootDegradedKeepsStrictAutonomy(t *testing.T) {
	store := storetest.NewMemStore()
	body := "[policy]\nautonomy_profile = \"strict\"\n" + routingLooser
	cfg, b := bootCheck(t, store, filepath.Join(t.TempDir(), "config.toml"), body, nil)
	if !b.Outcome.Degraded() {
		t.Fatalf("looser config with no record: %s, want degraded", b.Detail)
	}
	if got := runtime.EffectiveConfigOf(cfg).Policy.AutonomyProfile; got != "strict" {
		t.Fatalf("degraded boot of a strict config runs autonomy_profile %q (absent resolves to balanced)", got)
	}
}

// syncClass returns the class sync.status reports for domain/subkind.
func syncClass(t *testing.T, reg *rpc.Registry, domain, subkind string) string {
	t.Helper()
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	res, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: "sync.status"})
	if eo != nil {
		t.Fatalf("sync.status: %v", eo)
	}
	raw, _ := json.Marshal(res)
	var st struct {
		Domains []struct{ Domain, Subkind, Class string } `json:"domains"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	for _, d := range st.Domains {
		if d.Domain == domain && d.Subkind == subkind {
			return d.Class
		}
	}
	t.Fatalf("sync.status has no %s/%s: %s", domain, subkind, raw)
	return ""
}

func TestDaemonBootDivergentSyncWideningNotServed(t *testing.T) {
	deps := freshDaemonHome(t)
	reg, closeA := composeBaselineDaemon(t, deps, tightToml)
	builtin := syncClass(t, reg, "config", "config")
	closeA()
	if builtin == "server-primary" {
		t.Fatal("fixture: config/config is already server-primary, the widening proves nothing")
	}
	reg, _ = composeBaselineDaemon(t, deps, tightToml+"[sync]\n\"config/config\" = \"server-primary\"\n")
	if _, _, detail := baselineStatus(t, reg); detail != "baseline_divergent" {
		t.Fatalf("detail %q, want baseline_divergent", detail)
	}
	if got := syncClass(t, reg, "config", "config"); got != "local-only" {
		t.Fatalf("divergent [sync] widening: sync.status serves %q (built-in %q), want local-only", got, builtin)
	}
}

// reloadEvents records config.reload.* events.
type reloadEvents struct {
	mu       sync.Mutex
	rejected []string
}

func (r *reloadEvents) Publish(_ context.Context, name string, payload map[string]interface{}) {
	if name != "config.reload.rejected" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	reason, _ := payload["reason"].(string)
	r.rejected = append(r.rejected, reason)
}

func (r *reloadEvents) waitRejections(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := append([]string(nil), r.rejected...)
		r.mu.Unlock()
		if len(got) >= n {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waited 10s for %d reload rejection(s)", n)
	return nil
}

// Both tokenless reload paths, run for real with an ARMED slot for the very
// file on disk: the fsnotify watcher (wired in production by wireHotReload)
// and runtime.RegisterSIGHUP's handler (no production caller registers it;
// see the findings file). Neither may consume the slot.
func TestWatcherAndSIGHUPReloadsNeverUseTheSlot(t *testing.T) {
	f := newAcceptFixture(t)
	events := &reloadEvents{}
	hr := runtime.NewHotReloader(f.path, testLoadOpts(f.path), f.hr.Current(), runtime.NewSystemClock(), events, nil, nil)
	f.b.attachReloader(hr, testLoadOpts(f.path))
	accepted := runtime.EffectiveConfig{Sync: runtime.SyncSection{Classes: map[string]string{}}, Conductor: runtime.ConductorSection{ExternalRoutingEnabled: true}}
	f.b.slot.arm(runtime.ComputeSectionsHash(accepted), "ref")

	watcher := runtime.NewReloadWatcher(hr, f.path, 0)
	if err := watcher.Start(); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, f.path, routingLooser)
	got := events.waitRejections(t, 1)
	watcher.Stop()
	stop := runtime.RegisterSIGHUP(hr)
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		stop()
		t.Fatal(err)
	}
	got = events.waitRejections(t, len(got)+1)
	stop()
	for _, reason := range got {
		if !strings.Contains(reason, "elevation required") {
			t.Fatalf("a tokenless reload was rejected for another reason: %q", reason)
		}
	}
	if boolKey(hr.Current(), "external_routing_enabled") {
		t.Fatal("a tokenless reload applied the loosening")
	}
	if !f.slotArmed() {
		t.Fatal("a tokenless reload consumed the slot")
	}
}

func TestAcceptLooseningRefusesEnvOverrideOfGuardedValue(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	opts := testLoadOpts(f.path)
	opts.Environ = func() []string { return []string{"CASCADE_CONDUCTOR__SPILL_ENABLED=true"} }
	f.b.loadOpts = opts
	before := f.rows()
	err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(routingLooser), nil)
	if rpcCode(err) != cascade.RPCCodeConflict {
		t.Fatalf("env override of a guarded value at accept: %v, want conflict", err)
	}
	assertUnchanged(t, f, before)
}

// Two acceptances with no restart between both report the loosened key:
// the running process still has its boot values.
func TestAcceptLooseningTwiceReportsRestartKeysBothTimes(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	for i := 1; i <= 2; i++ {
		var out acceptLooseningResult
		if err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(routingLooser), &out); err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		if got := strings.Join(out.Reload.RestartRequiredKeys, ","); got != "conductor.external_routing_enabled" {
			t.Fatalf("accept %d: restart_required_keys = %q, want conductor.external_routing_enabled", i, got)
		}
	}
}
