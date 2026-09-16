package supervision

// Purpose (this file): the refusal directions — R-14.247 §4's three
//   failure cases, one test each, plus the constructor and key refusals
//   that hold Art.1 at the edges. They live apart from
//   dryrun_enforce_test.go's mainline guarantees for the 300-line cap.
//   "Block on any error" is the cheap reading; each test asserts the
//   direction the ruling actually fixed, which is why they are separate
//   questions and separate tests.
// SPORT: fleet.supervision dry-run-first direction tests (ADD)
//   — P1-E18-W4-S39-T4, R-14.247 §4.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestASimulationFailureBlocksAndLeavesNoFlag is the fail-closed direction.
// A dry run that could not complete has not shown the action is safe, so
// live execution is refused — and crucially no flag is written, or the very
// next attempt would run live having never been simulated at all.
func TestASimulationFailureBlocksAndLeavesNoFlag(t *testing.T) {
	ctx := context.Background()
	fx := newGuard(t, nil)
	fx.sim.err = cascade.New(cascade.KindUnavailable, "policy: engine unavailable")

	err := fx.guard.Guard(ctx, sampleRequest())
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied blocking the action", err)
	}
	if !strings.Contains(err.Error(), DryRunFirstBlockedCode) {
		t.Errorf("err = %q, want it to carry the greppable %s code", err, DryRunFirstBlockedCode)
	}
	if len(fx.attention.items) != 1 {
		t.Fatalf("%d attention items queued, want exactly 1 — a blocked action nobody is told about is a stall",
			len(fx.attention.items))
	}

	// No flag: the next attempt must simulate again rather than run live.
	fx.sim.err = nil
	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("the retry after a failed dry run: %v", err)
	}
	if len(fx.sim.calls) != 2 {
		t.Errorf("%d simulations in total, want the failed one AND the retry; a flag was written on failure",
			len(fx.sim.calls))
	}
}

// TestAnUnreadableFlagIsUnknownAndRunsTheDryRun is R-14.247 §4's first
// direction, and the one a "block on any error" reading gets wrong. An
// unreadable store means we do not KNOW whether the dry run happened. The
// conservative answer is to run it — re-running a simulation that writes
// nothing costs nothing, while blocking would let a broken store take the
// operator's autonomy away entirely.
func TestAnUnreadableFlagIsUnknownAndRunsTheDryRun(t *testing.T) {
	ctx := context.Background()
	fx := newGuard(t, readBrokenStore{Store: storetest.NewMemStore()})

	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("an unreadable flag blocked the action: %v", err)
	}
	if len(fx.sim.calls) != 1 {
		t.Errorf("%d simulations, want 1 — an unreadable flag must never be read as 'already done'",
			len(fx.sim.calls))
	}
}

// TestAnUnwritableFlagDoesNotBlockButIsReported is the third direction. The
// dry run HAPPENED, so the guard's promise — never fire live on the first
// attempt without a simulation — is kept, and failing the action here would
// be fail-closed on preference rather than on authorization (R-14.245).
// What the operator must learn is that the guard will fire again.
func TestAnUnwritableFlagDoesNotBlockButIsReported(t *testing.T) {
	ctx := context.Background()
	fx := newGuard(t, writeBrokenStore{Store: storetest.NewMemStore()})

	if err := fx.guard.Guard(ctx, sampleRequest()); err != nil {
		t.Fatalf("an unwritable flag blocked an action whose dry run succeeded: %v", err)
	}
	if len(fx.sim.calls) != 1 {
		t.Fatalf("%d simulations, want 1", len(fx.sim.calls))
	}
	if len(fx.attention.items) != 1 {
		t.Errorf("%d attention items, want 1 telling the operator the guard will fire again",
			len(fx.attention.items))
	}
}

// TestTheGuardRefusesToBeBuiltIncomplete holds Art.1 at the constructor. A
// guard missing any one of its three required collaborators could only
// proceed by assuming, and every assumption available breaks the guarantee
// in one direction or the other.
func TestTheGuardRefusesToBeBuiltIncomplete(t *testing.T) {
	flags, err := NewFirstRunFlags(storetest.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	sim := &recordingSimulator{}
	profiles := &staticProfiles{}

	cases := map[string]struct {
		sim      DryRunSimulator
		flags    *FirstRunFlags
		profiles ProfileReader
	}{
		"no simulator":  {nil, flags, profiles},
		"no flag store": {sim, nil, profiles},
		"no profiles":   {sim, flags, nil},
	}
	for name, tc := range cases {
		got, err := EnforceDryRunFirst(tc.sim, tc.flags, tc.profiles, nil, nil)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: err = %v, want KindInvalidInput", name, err)
		}
		if got != nil {
			t.Errorf("%s: a refused build still returned a guard", name)
		}
	}
	if _, err := NewFirstRunFlags(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("NewFirstRunFlags(nil): err = %v, want KindInvalidInput", err)
	}
}

// TestTheFlagKeyNeedsBothHalves keeps one pair's flag from becoming every
// pair's flag. An empty half would collapse distinct keys onto one another,
// which is the same defect as no key at all.
func TestTheFlagKeyNeedsBothHalves(t *testing.T) {
	ctx := context.Background()
	flags, err := NewFirstRunFlags(storetest.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"", "v1"}, {"acct", ""}, {"", ""}} {
		if _, err := flags.Done(ctx, pair[0], pair[1]); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("Done(%q,%q): err = %v, want KindInvalidInput", pair[0], pair[1], err)
		}
		if err := flags.Mark(ctx, pair[0], pair[1]); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("Mark(%q,%q): err = %v, want KindInvalidInput", pair[0], pair[1], err)
		}
	}
	// And two distinct pairs must not share a key.
	if FirstRunKey("a", "b") == FirstRunKey("a:b", "") {
		t.Error("two distinct pairs produced the same flag key")
	}
}

// TestTheGuardDegradesWithoutItsOptionalSinks proves the two nil-able
// collaborators are genuinely optional: a partially-wired daemon gets a
// shorter trail, never a different decision.
func TestTheGuardDegradesWithoutItsOptionalSinks(t *testing.T) {
	ctx := context.Background()
	flags, err := NewFirstRunFlags(storetest.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	sim := &recordingSimulator{err: cascade.New(cascade.KindUnavailable, "engine down")}
	g, err := EnforceDryRunFirst(sim, flags, &staticProfiles{p: namedProfile(t, "balanced")}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The blocking decision must be identical with no attention queue and
	// no audit writer attached.
	if err := g.Guard(ctx, sampleRequest()); !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want the same KindPolicyDenied block", err)
	}
}
