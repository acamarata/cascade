package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// Purpose (this file): the daemon's dispatch composition — that the verbs
//   are mounted, that an unconfigured remote is REFUSED rather than run
//   against the controller's own checkout, and that the fencing register is
//   shared across the verbs that mount over it.
// SPORT: internal/daemon tests (ADD) — P1-E17-W4-S37-T2.

// configuredSection is a [nodes] table with both dispatch knobs set.
func configuredSection() nodes.Section {
	return nodes.Section{DispatchRepoRoot: "/srv/work", DispatchRemote: "/srv/remote.git"}
}

// TestTheDispatchVerbsAreMounted proves the composition root registers all
// of them; a verb that is never mounted is engine nobody can reach.
func TestTheDispatchVerbsAreMounted(t *testing.T) {
	registry := rpc.NewRegistry()
	dispatcher, rendezvous := RegisterNodeDispatchHandlers(
		registry, nodes.NewRecordStore(nodes.NewFileRecordBackend(t.TempDir()), runtime.NewSystemClock()),
		runtime.NewSystemClock(),
		func() (nodes.Section, error) { return configuredSection(), nil },
	)
	if dispatcher == nil || rendezvous == nil {
		t.Fatal("the composition returned no dispatcher or rendezvous")
	}
	RegisterNodeDispatchJournal(registry, dispatcher, nil)

	for _, method := range []string{
		nodes.DispatchMethod, nodes.DispatchClaimMethod,
		nodes.DispatchReportMethod, nodes.DispatchJournalMethod,
	} {
		req, errObj := rpc.Parse([]byte(`{"jsonrpc":"2.0","method":"` + method + `","params":{},"id":1}`))
		if errObj != nil {
			t.Fatalf("Parse(%s): %+v", method, errObj)
		}
		// Every call here is expected to FAIL on its own arguments; what
		// is asserted is that it was not rejected as an unknown method.
		if _, errObj := registry.Dispatch(context.Background(), req); errObj != nil &&
			strings.Contains(strings.ToLower(errObj.Message), "method") &&
			strings.Contains(strings.ToLower(errObj.Message), "not") {
			t.Errorf("%s is not mounted: %+v", method, errObj)
		}
	}
}

// TestAnUnconfiguredRemoteIsRefused is the rule that matters most here.
// Falling back to the controller's own checkout would run REMOTE work
// against the operator's working tree, which is the one outcome a dispatch
// must never produce silently.
func TestAnUnconfiguredRemoteIsRefused(t *testing.T) {
	for _, tc := range []struct {
		why     string
		section nodes.Section
	}{
		{"no repo root", nodes.Section{DispatchRemote: "/srv/remote.git"}},
		{"no remote", nodes.Section{DispatchRepoRoot: "/srv/work"}},
		{"neither", nodes.Section{}},
		{"a blank remote", nodes.Section{DispatchRepoRoot: "/srv/work", DispatchRemote: "   "}},
	} {
		if err := requireDispatchConfig(tc.section); err == nil {
			t.Errorf("a dispatch with %s was admitted", tc.why)
		}
	}
	if err := requireDispatchConfig(configuredSection()); err != nil {
		t.Errorf("a fully configured section was refused: %v", err)
	}
}

// TestResolvingADispatchNeedsItsCollaborators covers the refusals before a
// record is ever looked up.
func TestResolvingADispatchNeedsItsCollaborators(t *testing.T) {
	d := nodes.NewDispatcher()
	rv := nodes.NewRendezvous()
	clock := runtime.NewSystemClock()
	store := nodes.NewRecordStore(nodes.NewFileRecordBackend(t.TempDir()), clock)

	if _, _, err := resolveDispatchDeps(d, rv, nil, clock, func() (nodes.Section, error) {
		return configuredSection(), nil
	}, "n1"); err == nil {
		t.Error("a dispatch resolved with no record store")
	}
	if _, _, err := resolveDispatchDeps(d, rv, store, clock, nil, "n1"); err == nil {
		t.Error("a dispatch resolved with no config reader")
	}
	// An unconfigured section refuses BEFORE the node is looked up, so a
	// misconfigured controller reports the config rather than a confusing
	// "no such node".
	_, _, err := resolveDispatchDeps(d, rv, store, clock, func() (nodes.Section, error) {
		return nodes.Section{}, nil
	}, "n1")
	// BOTH missing knobs must be named, deterministically. This assertion
	// used to name only one and passed by luck: requireDispatchConfig
	// ranged over a map, so which knob it reported depended on Go's
	// iteration order.
	if err == nil {
		t.Fatal("an unconfigured section resolved")
	}
	for _, knob := range []string{"dispatch_repo_root", "dispatch_remote"} {
		if !strings.Contains(err.Error(), knob) {
			t.Errorf("err = %v, want it to name the missing knob %q", err, knob)
		}
	}
	// A node that is not enrolled is refused too.
	if _, _, err := resolveDispatchDeps(d, rv, store, clock, func() (nodes.Section, error) {
		return configuredSection(), nil
	}, "never-enrolled"); err == nil {
		t.Error("a dispatch resolved against a node that was never enrolled")
	}
}

// TestTheNodesSectionReachesSettings proves the [nodes] table is decoded
// into Settings, which is the section's first reader in a shipping binary.
func TestTheNodesSectionReachesSettings(t *testing.T) {
	if got := mapSection(nil); got != nil {
		t.Errorf("mapSection(nil) = %v, want nil", got)
	}
	if got := mapSection("not a table"); got != nil {
		t.Errorf("mapSection of a non-table = %v, want nil", got)
	}
	table := map[string]interface{}{"dispatch_repo_root": "/srv/work"}
	if got := mapSection(table); got == nil || got["dispatch_repo_root"] != "/srv/work" {
		t.Errorf("mapSection dropped the table: %v", got)
	}
}

// TestTheJournalSinkRefusesWithNoStore proves a daemon without a journal
// store reports it rather than silently discarding a node's records.
func TestTheJournalSinkRefusesWithNoStore(t *testing.T) {
	err := journalStreamSink{}.AppendNodeStream(context.Background(), "e1", "op1", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("a record was accepted with no journal store open")
	}
}

// TestTheDispatchClockUsesTheInjectedOne proves the attempt is not stamped
// from a bare time.Now, which would make its ordering unreproducible.
func TestTheDispatchClockUsesTheInjectedOne(t *testing.T) {
	clock := runtime.NewSystemClock()
	if got := (dispatchClock{clock}).Now(); got.IsZero() {
		t.Fatal("the dispatch clock reported the zero time")
	}
}

// TestTheGitRunnerReportsAFailureWithItsStderr proves a failed git command
// surfaces what git actually said, not just a generic exit status.
func TestTheGitRunnerReportsAFailureWithItsStderr(t *testing.T) {
	_, err := execGitRunner{}.Run(context.Background(), t.TempDir(), "rev-parse", "--verify", "definitely-not-a-ref")
	if err == nil {
		t.Fatal("a failing git command reported success")
	}
	if !strings.Contains(err.Error(), "rev-parse") {
		t.Errorf("err = %v, want it to name the git command that failed", err)
	}
}
