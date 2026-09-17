// Purpose (this file): the two CLI-side guards on a discarding
//
//	resolution, and the views' rendering of the facts an operator reads.
//
// WHY THE GUARDS GET THEIR OWN ASSERTIONS: each one refuses BEFORE the
//
//	elevation gate, and "refused, but asked the operator for a
//	fingerprint first" is a real failure that a pass/fail-only assertion
//	cannot see. Every case here checks the gate was never consulted.
//
// SPORT: cmd/cascade sync guards + views (ADD tests) — P1-E17-W4-S38-T3.
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	syncpkg "github.com/acamarata/cascade/internal/sync"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestResolveKeepLocalIsRefusedOnWindows pins the tier-2 platform rule.
// Asserted here with an injected GOOS so every lane runs it, and again on
// the Windows lane itself in sync_windows_test.go (R-14.131: a
// platform-specific behavior needs a test that RUNS on that platform).
func TestResolveKeepLocalIsRefusedOnWindows(t *testing.T) {
	gate := &countingGate{}
	deps := testSyncDeps(gate)
	deps.GOOS = "windows"
	_, err := runSyncCmd(t, newSyncConflictsResolveCmd(deps), "cfg-3", "--keep", "local")
	if !isCLIKind(err, cascade.KindUnsupported) {
		t.Fatalf("resolve --keep local on windows = %v, want the tier-2 refusal (KindUnsupported)", err)
	}
	if len(gate.verbs) != 0 {
		t.Errorf("the tier-2 refusal still consulted the elevation gate: %v", gate.verbs)
	}
}

// TestKeepServerIsAllowedOnWindows proves the tier-2 refusal is scoped to
// the side that discards data. A platform rule that also blocked the
// harmless verb would make a Windows operator unable to close a conflict
// at all.
func TestKeepServerIsAllowedOnWindows(t *testing.T) {
	deps := testSyncDeps(&countingGate{})
	deps.GOOS = "windows"
	if _, err := runSyncCmd(t, newSyncConflictsResolveCmd(deps), "cfg-3", "--keep", "server"); err != nil {
		t.Fatalf("resolve --keep server on windows = %v, want it allowed", err)
	}
}

// TestResolveKeepLocalUnderNoInputRequiresYes covers the non-interactive
// rule: a machine that cannot ask must be TOLD, not assumed at.
func TestResolveKeepLocalUnderNoInputRequiresYes(t *testing.T) {
	gate := &countingGate{}
	deps := testSyncDeps(gate)
	deps.GOOS = "linux"
	deps.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	_, err := runSyncCmd(t, newSyncConflictsResolveCmd(deps), "cfg-3", "--keep", "local")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("resolve --keep local under CASCADE_NO_INPUT=1 = %v, want KindElevationRequired", err)
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("the refusal does not name the flag that would satisfy it: %v", err)
	}
	if len(gate.verbs) != 0 {
		t.Errorf("the no-input refusal still consulted the elevation gate: %v", gate.verbs)
	}

	// With --yes it proceeds, through the gate. Without this half the
	// assertion above would pass for a guard that refused unconditionally.
	if _, err := runSyncCmd(t, newSyncConflictsResolveCmd(deps), "cfg-3", "--keep", "local", "--yes"); err != nil {
		t.Fatalf("resolve --keep local --yes under CASCADE_NO_INPUT=1 = %v, want it allowed", err)
	}
	if len(gate.verbs) != 1 {
		t.Errorf("the gate saw %v, want the one elevated verb", gate.verbs)
	}
}

// TestNoInputDoesNotGateTheHarmlessSide proves the non-interactive rule,
// like the platform one, is scoped to the discarding side.
func TestNoInputDoesNotGateTheHarmlessSide(t *testing.T) {
	deps := testSyncDeps(&countingGate{})
	deps.GOOS = "linux"
	deps.Getenv = func(string) string { return "1" }
	if _, err := runSyncCmd(t, newSyncConflictsResolveCmd(deps), "cfg-3", "--keep", "server"); err != nil {
		t.Fatalf("resolve --keep server under CASCADE_NO_INPUT=1 = %v, want it allowed", err)
	}
}

// TestSyncViewsRenderTheMissingValues covers the renderings the happy
// paths never reach: a conflict carried by a git ref, one a merge refused
// outright, and a run with nothing to do.
func TestSyncViewsRenderTheMissingValues(t *testing.T) {
	v := syncConflictsView{syncpkg.ConflictsResult{Conflicts: []syncpkg.Conflict{
		{RecordID: "refs/heads/main", Domain: "config", Subkind: "phase-state",
			Winner: syncpkg.Side{Ref: "refs/heads/main"}},
		{RecordID: "cfg-9", Domain: "config", Subkind: "config"},
	}}}
	got := v.String()
	if !strings.Contains(got, "refs/heads/main") {
		t.Errorf("a git-carried conflict lost its ref:\n%s", got)
	}
	if !strings.Contains(got, "-") {
		t.Errorf("a side with neither ref nor hash rendered as an empty column:\n%s", got)
	}
	if empty := (syncRunView{syncpkg.RunResult{}}).String(); !strings.Contains(empty, "no domain") {
		t.Errorf("an empty run rendered as %q", empty)
	}
}

// TestSyncStatusViewPrintsAKnownPosition is the other half of the
// unknown-position assertion: a position that WAS read renders as the
// number, not as "?".
func TestSyncStatusViewPrintsAKnownPosition(t *testing.T) {
	got := syncStatusView{syncpkg.StatusResult{PeerTier: "controller", Domains: []syncpkg.DomainStatus{
		{Domain: "config", Subkind: "config", Strategy: "server-primary-lww",
			Eligible: true, Position: 41, PositionKnown: true},
		{Domain: "memory", Subkind: "memory", Eligible: false, Reason: "tier may not"},
	}}}.String()
	if !strings.Contains(got, "41") {
		t.Errorf("a known position was not printed:\n%s", got)
	}
	if !strings.Contains(got, "tier may not") {
		t.Errorf("an ineligible domain's reason was not printed:\n%s", got)
	}
}

// TestOpenSyncStoreReturnsTheInterfaceNotTheDriver is the regression test
// for a real panic. openSyncStore returning *sqlite.Driver put a TYPED
// nil into provider.Store on every machine whose store would not open:
// the value was nil, the interface was not, CursorStore.Get's `== nil`
// guard passed, and `cascade sync status` panicked inside the driver.
// Asserted on the SIGNATURE because that is where the bug lived — the
// failing return is hard to induce and trivial to reintroduce.
func TestOpenSyncStoreReturnsTheInterfaceNotTheDriver(t *testing.T) {
	out := reflect.TypeOf(openSyncStore).Out(0)
	if out.Kind() != reflect.Interface {
		t.Fatalf("openSyncStore returns %s (%s); returning a concrete pointer puts a typed nil in "+
			"provider.Store, which no == nil guard downstream can catch", out, out.Kind())
	}
	if out != reflect.TypeOf((*provider.Store)(nil)).Elem() {
		t.Errorf("openSyncStore returns %s, want provider.Store", out)
	}
}

// TestProductionSyncDepsWiresWhatItClaims covers the composition root.
// Run is DELIBERATELY nil (the session transport is S-38.T5's), and that
// is asserted rather than left to drift: a future wiring that filled it
// in without a real session would make `sync run` report syncs nobody did.
func TestProductionSyncDepsWiresWhatItClaims(t *testing.T) {
	// The real composition opens a real store under CASCADE_HOME, so the
	// test gives it one of its own. A unit test that wrote into the
	// developer's own ~/.cascade would be a side effect nobody asked for.
	home := t.TempDir()
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade"))
	deps := productionSyncDeps()
	if deps.OpenEngine == nil {
		t.Fatal("production wired no engine opener; status and conflicts are built entirely on it")
	}

	// DESCRIBING the command must touch nothing. The composition root
	// builds every tree on every invocation, so an engine opened here
	// would create ~/.cascade for an operator who typed `cascade --help`.
	// Opening it eagerly did exactly that, and turned six unrelated
	// "this command wrote nothing" tests red at once.
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("building the sync deps created %v in a clean home (err=%v)", entries, err)
	}

	// Opened AND CLOSED: the opener hands back the closer precisely so a
	// caller can give the file handle back, and a test that leaked one
	// would be asserting the behaviour this ticket fixed while
	// reproducing the bug (Windows cannot delete an open sqlite file, so
	// the leak surfaced there as a failed TempDir cleanup).
	engine, closer := deps.OpenEngine()
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}
	if engine == nil || engine.Conflicts() == nil {
		t.Error("the opened engine has no conflict journal")
	}
	if closer == nil {
		t.Error("the production opener returned no closer; whatever it opened cannot be given back")
	}
	if deps.PeerTier == "" {
		t.Error("production wired no peer tier; status could not say which question it answered")
	}
	if deps.Getenv == nil {
		t.Error("production wired no Getenv; CASCADE_NO_INPUT would read as unset on every machine")
	}
	if deps.Run != nil {
		t.Error("production wired a run path; nothing in this tree opens a sync session yet")
	}
}
