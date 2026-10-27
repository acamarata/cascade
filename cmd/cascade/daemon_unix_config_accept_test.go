//go:build !windows

// Purpose: proves config.accept_loosening accepts only the exact on-disk
// file under a verified attestation for itself, writes a signed baseline
// that boots, and lets exactly one hot reload of those values through.
// Inputs: the daemon registry with the real elevation middleware over an
// injected in-memory custody (newElevatedDaemon), temp config files.
// Outputs: none (tests).
// Constraints: the witness comes only from the middleware; no keychain.
// SPORT: cmd/cascade config.accept_loosening tests (P1-SEC-04).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type acceptFixture struct {
	d    elevatedDaemon
	b    *configBaseline
	hr   *runtime.HotReloader
	path string
}

// newAcceptFixture boots tightToml through checkBootBaseline over the
// elevated daemon's store, with the production verifier over its custody,
// and serves config.accept_loosening from that baseline.
func newAcceptFixture(t *testing.T) acceptFixture {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	d := newElevatedDaemon(t, dataDir, true)
	path := filepath.Join(t.TempDir(), "config.toml")
	clock := runtime.NewSystemClock()
	cfg, b := bootCheck(t, d.Store, path, tightToml, productionBaselineVerifier(dataDir, clock))
	hr := runtime.NewHotReloader(path, testLoadOpts(path), cfg, clock, nil, nil, nil)
	b.attachReloader(hr, testLoadOpts(path))
	d.Registry.Register(runtime.AcceptLooseningMethod, b.acceptLoosening)
	return acceptFixture{d: d, b: b, hr: hr, path: path}
}

func digestParams(body string) map[string]string {
	return map[string]string{"config_sha256": runtime.ConfigDigest([]byte(body))}
}

// rows returns the persisted baseline rows (nil when absent).
func (f acceptFixture) rows() [2][]byte {
	var out [2][]byte
	for i, k := range []string{"config_baseline", "config_baseline_snapshot"} {
		out[i], _ = f.d.Store.Get(context.Background(), "audit", k)
	}
	return out
}

func (f acceptFixture) slotArmed() bool {
	f.b.slot.mu.Lock()
	defer f.b.slot.mu.Unlock()
	return f.b.slot.token != nil
}

func rpcCode(err error) int {
	var eo *rpc.ErrorObject
	if errors.As(err, &eo) {
		return eo.Code
	}
	return 0
}

func assertUnchanged(t *testing.T, f acceptFixture, before [2][]byte) {
	t.Helper()
	after := f.rows()
	if !bytes.Equal(before[0], after[0]) || !bytes.Equal(before[1], after[1]) {
		t.Fatal("the baseline record or snapshot changed")
	}
	if f.slotArmed() {
		t.Fatal("a slot was armed")
	}
	if boolKey(f.hr.Current(), "external_routing_enabled") {
		t.Fatal("the looser value went live")
	}
}

func TestAcceptLooseningRefusesWithoutAttestation(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	before := f.rows()
	raw := []byte(`{"config_sha256":"` + runtime.ConfigDigest([]byte(routingLooser)) + `"}`)
	_, err := f.b.acceptLoosening(context.Background(), raw)
	if kind, _ := cascade.KindOf(err); err == nil || kind != cascade.KindElevationRequired {
		t.Fatalf("no witness: err %v, want elevation_required", err)
	}
	assertUnchanged(t, f, before)
}

func TestAcceptLooseningRefusesWrongWitness(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	before := f.rows()
	var handlerErr error
	runs := 0
	f.d.Registry.Register("vault.get", func(ctx context.Context, raw json.RawMessage) (any, error) {
		runs++
		if w, ok := rpc.VerifiedElevationFrom(ctx); !ok || w.Method() != "vault.get" {
			t.Errorf("the probe ran without a vault.get witness")
		}
		_, handlerErr = f.b.acceptLoosening(ctx, raw)
		return nil, handlerErr
	})
	_ = f.d.Call(context.Background(), "vault.get", digestParams(routingLooser), nil)
	if kind, _ := cascade.KindOf(handlerErr); runs != 1 || handlerErr == nil || kind != cascade.KindElevationRequired {
		t.Fatalf("runs=%d err=%v, want one run refused elevation_required", runs, handlerErr)
	}
	assertUnchanged(t, f, before)
}

func TestAcceptLooseningRefusesShaMismatch(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	before := f.rows()
	err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(tightToml), nil)
	if rpcCode(err) != cascade.RPCCodeConflict {
		t.Fatalf("param digest of another file: err %v, want conflict", err)
	}
	assertUnchanged(t, f, before)
}

func TestAcceptLooseningAppliesOnce(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	var out acceptLooseningResult
	if err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(routingLooser), &out); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !out.Reload.Accepted || out.Reload.ElevationRef == "" || !boolKey(f.hr.Current(), "external_routing_enabled") {
		t.Fatalf("accepted file did not apply: %+v", out)
	}
	// Guarded consumers read their values at boot: the loosened key is
	// reported as taking effect at the next start, not live.
	if got := strings.Join(out.Reload.RestartRequiredKeys, ","); got != "conductor.external_routing_enabled" {
		t.Fatalf("restart_required_keys = %q, want exactly conductor.external_routing_enabled", got)
	}
	if f.slotArmed() {
		t.Fatal("the slot is still armed after its one reload")
	}
	// The signed record boots: a restart keeps the accepted values.
	cfg, b := bootCheck(t, f.d.Store, f.path, routingLooser, productionBaselineVerifier(f.d.DataDir, runtime.NewSystemClock()))
	if b.Outcome != runtime.BaselineAccepted || !boolKey(cfg, "external_routing_enabled") {
		t.Fatalf("restart after accept: %s", b.Detail)
	}
	// Another loosening, by any reload without a fresh accept, is refused.
	writeConfig(t, f.path, routingLooser+"spill_enabled = true\n")
	if o := f.hr.Reload(context.Background()); !o.ElevationRequired {
		t.Fatalf("second loosening reload: %+v, want elevation required", o)
	}
	// The slot approves one reload per arming, even for the same values.
	slot := &loosenSlot{clock: runtime.NewSystemClock()}
	eff := runtime.EffectiveConfigOf(cfg)
	tok := slot.arm(runtime.ComputeSectionsHash(eff), "ref-1")
	ctx := withSlotToken(context.Background(), tok)
	if ref, err := slot.ApproveLoosening(ctx, eff, nil); err != nil || ref != "ref-1" {
		t.Fatalf("first approval: %q %v", ref, err)
	}
	if _, err := slot.ApproveLoosening(ctx, eff, nil); err == nil {
		t.Fatal("the slot approved twice")
	}
}

// TestReloadWithoutSlotStillRefusesLoosening calls Reload directly with a
// context that carries no slot token: the same call the fsnotify watcher and
// runtime.RegisterSIGHUP's handler make. It does not drive either real path;
// TestWatcherAndSIGHUPReloadsNeverUseTheSlot does.
func TestReloadWithoutSlotStillRefusesLoosening(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	if o := f.hr.Reload(context.Background()); !o.ElevationRequired {
		t.Fatalf("unaccepted loosening reload: %+v", o)
	}
	// An armed slot is still not usable by a reload that does not carry it
	// (fsnotify and SIGHUP call Reload with their own context).
	loose, _ := runtime.Load(context.Background(), testLoadOpts(f.path))
	f.b.slot.arm(runtime.ComputeSectionsHash(runtime.EffectiveConfigOf(loose)), "ref")
	if o := f.hr.Reload(context.Background()); !o.ElevationRequired {
		t.Fatalf("tokenless reload with an armed slot: %+v", o)
	}
	if o := f.hr.Reload(withSlotToken(context.Background(), &slotToken{})); !o.ElevationRequired {
		t.Fatalf("reload with a forged token: %+v", o)
	}
	if boolKey(f.hr.Current(), "external_routing_enabled") {
		t.Fatal("the looser value went live")
	}
}

func TestSlotExpiresAfterFiveMinutes(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	slot := &loosenSlot{clock: clock}
	eff := runtime.EffectiveConfig{Conductor: runtime.ConductorSection{SpillEnabled: true}}
	hash := runtime.ComputeSectionsHash(eff)

	tok := slot.arm(hash, "ref")
	clock.Advance(4*time.Minute + 59*time.Second)
	if _, err := slot.ApproveLoosening(withSlotToken(context.Background(), tok), eff, nil); err != nil {
		t.Fatalf("approval inside the window refused: %v", err)
	}
	tok = slot.arm(hash, "ref")
	clock.Advance(5*time.Minute + time.Second)
	if _, err := slot.ApproveLoosening(withSlotToken(context.Background(), tok), eff, nil); err == nil {
		t.Fatal("approval at +5m01s succeeded")
	}
}

func TestSlotRefusesSwappedFile(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	accepted, _ := runtime.Load(context.Background(), testLoadOpts(f.path))
	tok := f.b.slot.arm(runtime.ComputeSectionsHash(runtime.EffectiveConfigOf(accepted)), "ref")
	// Swapped after the digest check: different guarded values on disk.
	writeConfig(t, f.path, spillLooserToml)
	if o := f.hr.Reload(withSlotToken(context.Background(), tok)); !o.ElevationRequired {
		t.Fatalf("swapped file reload: %+v, want elevation required", o)
	}
	if boolKey(f.hr.Current(), "spill_enabled") {
		t.Fatal("the swapped file went live")
	}
}

func TestAcceptLooseningRefusesBadParamsAndUnloadable(t *testing.T) {
	f := newAcceptFixture(t)
	before := f.rows()
	if err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, map[string]string{"config_sha256": "ABC"}, nil); rpcCode(err) != cascade.RPCCodeInvalidInput {
		t.Fatalf("bad params: %v", err)
	}
	broken := "[conductor\n"
	writeConfig(t, f.path, broken)
	if err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(broken), nil); rpcCode(err) != cascade.RPCCodeInvalidInput {
		t.Fatalf("unloadable file: %v", err)
	}
	assertUnchanged(t, f, before)
	_ = os.Remove(f.path)
}

// putFailStore fails every Put.
type putFailStore struct{ provider.Store }

func (putFailStore) Put(context.Context, string, string, []byte) error {
	return errors.New("synthetic disk full")
}

func TestAcceptLooseningStoreErrorArmsNoSlot(t *testing.T) {
	f := newAcceptFixture(t)
	clock := runtime.NewSystemClock()
	f.b.Checker = runtime.NewBaselineChecker(putFailStore{f.d.Store}, clock, nil, runtime.NewStoreAuditRecorder(putFailStore{f.d.Store}, clock))
	f.b.Checker.SetVerifier(productionBaselineVerifier(f.d.DataDir, clock))
	writeConfig(t, f.path, routingLooser)
	before := f.rows()
	if err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(routingLooser), nil); rpcCode(err) != cascade.RPCCodeUnavailable {
		t.Fatalf("store failure: %v, want unavailable", err)
	}
	assertUnchanged(t, f, before)
}

func TestAcceptLooseningRefusesWorldWritableConfig(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	if err := os.Chmod(f.path, 0o666); err != nil {
		t.Fatal(err)
	}
	before := f.rows()
	err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(routingLooser), nil)
	if rpcCode(err) != cascade.RPCCodePermissionDenied {
		t.Fatalf("world-writable config.toml: err %v, want permission_denied", err)
	}
	assertUnchanged(t, f, before)
}
