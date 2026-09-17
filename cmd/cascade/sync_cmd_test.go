// Purpose (this file): `cascade sync`'s four verbs, driven through their
//
//	real cobra RunE bodies — the refusals especially, because every one
//	of them is a place where the easy implementation does the damage
//	silently.
//
// SPORT: cmd/cascade sync tests (ADD) — P1-E17-W4-S38-T3.
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	syncpkg "github.com/acamarata/cascade/internal/sync"
	"github.com/acamarata/cascade/pkg/cascade"
)

// countingGate records every verb it was asked to authorize, so a test
// can assert not only that a refusal happened but that it happened
// BEFORE the gate — an order that matters, because a CLI-side refusal
// which still consulted the gate would have already asked the operator
// for a fingerprint it was never going to use.
type countingGate struct {
	verbs []string
	err   error
}

func (g *countingGate) Authorize(_ context.Context, verb string) error {
	g.verbs = append(g.verbs, verb)
	return g.err
}

// testSyncDeps builds a sync surface with one journaled conflict and no
// store, which is the shape a machine that has never synced actually has.
func testSyncDeps(gate syncpkg.ElevationGate) syncDeps {
	engine := syncpkg.NewEngine(nil, runtime.NewSystemClock(), nil)
	engine.Conflicts().Record(syncpkg.Conflict{
		Domain: "config", Subkind: "config", RecordID: "cfg-3",
		Strategy: syncpkg.StrategyServerPrimaryLWW, Resolution: syncpkg.ResolutionServerWon,
		Winner: syncpkg.Side{NodeID: "server", Revision: 9, Hash: "hs"},
		Loser:  syncpkg.Side{NodeID: "laptop", Revision: 8, Hash: "hl"},
	})
	return syncDeps{Engine: engine, PeerTier: nodes.TierController, Gate: gate}
}

// runSyncCmd executes one verb and returns what it wrote.
func runSyncCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

// TestSyncStatusPrintsEveryDomainAndAnUnknownPosition is the report's
// whole reason for existing: a domain that is not syncing has to APPEAR,
// and a position nobody could read has to be visibly unread rather than
// rendered as the zero an operator would take for "never synced".
func TestSyncStatusPrintsEveryDomainAndAnUnknownPosition(t *testing.T) {
	out, err := runSyncCmd(t, newSyncStatusCmd(testSyncDeps(nil)))
	if err != nil {
		t.Fatalf("sync status: %v", err)
	}
	for _, want := range []string{"peer tier: controller", "open conflicts: 1", "config/config"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
	// One "?" per domain line: with no store open, no position is known.
	lines := strings.Count(out, "\n")
	if got := strings.Count(out, "?"); got == 0 || got > lines {
		t.Errorf("status rendered %d unknown positions over %d lines; want one per domain:\n%s", got, lines, out)
	}
	if strings.Contains(out, "\t") {
		t.Error("status emitted raw tabs; the table was never flushed through tabwriter")
	}
}

// TestSyncRunWithNoRunPathRefuses proves the production gap is REPORTED.
// Reporting a successful sync for a run path that does not exist is the
// one outcome this composition must never produce.
func TestSyncRunWithNoRunPathRefuses(t *testing.T) {
	out, err := runSyncCmd(t, newSyncRunCmd(testSyncDeps(nil)))
	if !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("sync run with no run path = %v (output %q), want KindUnavailable", err, out)
	}
	// The outcome TABLE is what a reader would take for a report of work
	// done; the refusal text mentions syncing only to say it did not.
	if strings.Contains(out, "RESULT") {
		t.Errorf("a run that did nothing rendered an outcome table:\n%s", out)
	}
}

// TestSyncRunRefusesAnUnknownDomain covers the typo path: a caller who
// named a domain that is not registered asked for something, and syncing
// everything instead would be a bug they would not find for weeks.
func TestSyncRunRefusesAnUnknownDomain(t *testing.T) {
	deps := testSyncDeps(nil)
	deps.Run = func(context.Context, storage.DomainID, string) error { return nil }
	_, err := runSyncCmd(t, newSyncRunCmd(deps), "--domain", "confgi")
	if !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("sync run --domain confgi = %v, want KindNotFound", err)
	}
	if !strings.Contains(err.Error(), "config/config") {
		t.Errorf("the refusal does not name what IS registered: %v", err)
	}
}

// TestSyncRunReportsEachDomainSeparately proves one domain's failure does
// not become the whole run's failure.
func TestSyncRunReportsEachDomainSeparately(t *testing.T) {
	deps := testSyncDeps(nil)
	deps.Run = func(_ context.Context, domain storage.DomainID, _ string) error {
		if domain == storage.DomainMemory {
			return cascade.New(cascade.KindUnavailable, "the remote is down")
		}
		return nil
	}
	out, err := runSyncCmd(t, newSyncRunCmd(deps))
	if err != nil {
		t.Fatalf("a run with one failing domain failed as a whole: %v", err)
	}
	if !strings.Contains(out, "the remote is down") {
		t.Errorf("the failing domain's reason is not reported:\n%s", out)
	}
	if !strings.Contains(out, "synced") {
		t.Errorf("the healthy domains are not reported as synced:\n%s", out)
	}
}

// TestSyncConflictsListRendersTheJournal covers both the empty and the
// populated rendering; an empty journal has to say so rather than print a
// bare header a reader would take for a broken command.
func TestSyncConflictsListRendersTheJournal(t *testing.T) {
	empty := syncDeps{Engine: syncpkg.NewEngine(nil, runtime.NewSystemClock(), nil), PeerTier: nodes.TierController}
	out, err := runSyncCmd(t, newSyncConflictsListCmd(empty))
	if err != nil {
		t.Fatalf("conflicts list on an empty journal: %v", err)
	}
	if !strings.Contains(out, "no conflicts journaled") {
		t.Errorf("an empty journal rendered as %q", out)
	}

	out, err = runSyncCmd(t, newSyncConflictsListCmd(testSyncDeps(nil)))
	if err != nil {
		t.Fatalf("conflicts list: %v", err)
	}
	for _, want := range []string{"cfg-3", "server@9 hs", "laptop@8 hl"} {
		if !strings.Contains(out, want) {
			t.Errorf("conflicts list is missing %q:\n%s", want, out)
		}
	}
}

// TestResolveKeepServerIsNotElevated proves re-affirming what the merge
// already decided does not ask anybody for anything.
func TestResolveKeepServerIsNotElevated(t *testing.T) {
	gate := &countingGate{}
	out, err := runSyncCmd(t, newSyncConflictsResolveCmd(testSyncDeps(gate)), "cfg-3", "--keep", "server")
	if err != nil {
		t.Fatalf("resolve --keep server: %v", err)
	}
	if len(gate.verbs) != 0 {
		t.Errorf("keeping the server's own decision consulted the elevation gate: %v", gate.verbs)
	}
	if !strings.Contains(out, "kept the server side") {
		t.Errorf("resolve did not say what it kept:\n%s", out)
	}
}

// TestResolveKeepLocalGoesThroughTheGate proves the discarding side is
// gated, and that the verb the gate sees names what is being discarded.
func TestResolveKeepLocalGoesThroughTheGate(t *testing.T) {
	gate := &countingGate{}
	out, err := runSyncCmd(t, newSyncConflictsResolveCmd(testSyncDeps(gate)), "cfg-3", "--keep", "local")
	if err != nil {
		t.Fatalf("resolve --keep local: %v", err)
	}
	if len(gate.verbs) != 1 || gate.verbs[0] != syncpkg.ElevatedVerbResolve {
		t.Fatalf("the gate saw %v, want exactly [%s]", gate.verbs, syncpkg.ElevatedVerbResolve)
	}
	if !strings.Contains(out, "elevated") {
		t.Errorf("an elevated resolution did not say so:\n%s", out)
	}
}

// TestResolveKeepLocalWithNoGateIsRefused is the rule that a machine
// which cannot check must not be the machine that allows it.
func TestResolveKeepLocalWithNoGateIsRefused(t *testing.T) {
	_, err := runSyncCmd(t, newSyncConflictsResolveCmd(testSyncDeps(nil)), "cfg-3", "--keep", "local")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("resolve --keep local with no gate = %v, want KindElevationRequired", err)
	}
}

// TestResolveRefusesAnUnknownConflict keeps `resolve` from reporting that
// it settled something the journal never carried.
func TestResolveRefusesAnUnknownConflict(t *testing.T) {
	_, err := runSyncCmd(t, newSyncConflictsResolveCmd(testSyncDeps(&countingGate{})), "no-such", "--keep", "server")
	if !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("resolve of an unjournaled record = %v, want KindNotFound", err)
	}
}

// TestResolveRefusesASideItDoesNotKnow proves --keep is validated rather
// than defaulted; defaulting would pick a side on the operator's behalf.
func TestResolveRefusesASideItDoesNotKnow(t *testing.T) {
	for _, keep := range []string{"", "mine", "SERVER"} {
		args := []string{"cfg-3"}
		if keep != "" {
			args = append(args, "--keep", keep)
		}
		_, err := runSyncCmd(t, newSyncConflictsResolveCmd(testSyncDeps(&countingGate{})), args...)
		if !isCLIKind(err, cascade.KindInvalidInput) {
			t.Errorf("resolve --keep %q = %v, want KindInvalidInput", keep, err)
		}
	}
}
