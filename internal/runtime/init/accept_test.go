//go:build integration

package init_test

// Purpose: scenarios A and C of P1-E16-W4-S35-T5 — a fresh `cascade init`
//   leaves the harness wired, and a second run converges instead of
//   overwriting.
// Constraints: every assertion reads a file the real binary wrote, or a
//   row the real binary printed. Nothing here inspects the wizard's own
//   in-process state: this suite exists precisely because the unit tests
//   could all pass while the program wired nothing.
// SPORT: internal/runtime/init acceptance (ADD) — P1-E16-W4-S35-T5.

import (
	"strings"
	"testing"
)

// initArgs is the non-interactive setup invocation these scenarios use.
//
// --no-daemon is not a weakening of the story: installing a launchd or
// systemd unit on a CI runner leaves a service behind after the test
// process exits, which is a side effect an acceptance suite must not
// have. The daemon step's three skip reasons are unit-tested, and
// scenario D starts a daemon directly rather than installing one.
var initArgs = []string{"init", "--yes", "--no-daemon"}

// TestAcceptInitScenarioA: on a machine with the harness installed and no
// prior cascade configuration, `cascade init --yes` leaves that harness
// FULLY wired — instruction file, hook pack, and MCP entry — and the
// surface an operator checks says so.
//
// All four assertions, not one. Before this suite existed the wizard
// printed "claude wired" while writing nothing at all: the instruction
// generator had no tier to render, the hook pack and MCP entry were never
// called, and the two that were called resolved into a different
// product's directory. Each of those passes three of the four checks
// below.
func TestAcceptInitScenarioA(t *testing.T) {
	e := newEnv(t)

	out, code := e.run(t, initArgs...)
	if code != 0 {
		t.Fatalf("cascade init --yes exited %d:\n%s", code, out)
	}
	requireContains(t, out, "claude", "the run reports the harness it wired")

	if !exists(e.instructionFile()) {
		t.Errorf("no instruction file at %s; the harness has nothing to read", e.instructionFile())
	}
	if !exists(e.hookPack()) {
		t.Errorf("no hook pack at %s; the harness emits no session events", e.hookPack())
	}
	if names := e.mcpServerNames(t); !contains(names, "cascade") {
		t.Errorf("the harness user config registers %v, want cascade among them", names)
	}

	row := e.harnessRow(t, "claude")
	if !row.Detected {
		t.Errorf("harness list reports claude undetected after a successful wire")
	}
	if !row.CascadeRegistered {
		t.Errorf("harness list reports claude unregistered after a successful wire; " +
			"this is the exact false success the scenario exists to catch")
	}
}

// TestAcceptInitLeavesNoJournalBehind: the resume journal's presence IS
// the signal that a run was interrupted, so a completed run must delete
// it. A leftover journal makes the next run report a resume and skip
// steps.
func TestAcceptInitLeavesNoJournalBehind(t *testing.T) {
	e := newEnv(t)

	if out, code := e.run(t, initArgs...); code != 0 {
		t.Fatalf("cascade init --yes exited %d:\n%s", code, out)
	}
	if exists(e.journal()) {
		t.Errorf("the journal survived a completed run at %s", e.journal())
	}
}

// TestAcceptInitScenarioC: a second run converges. It exits 0 (nothing to
// do) or 3 (work reported), it never silently overwrites an instruction
// file somebody edited, and the harness is still wired afterwards.
//
// The sentinel is the load-bearing part. "Exits 0 twice" is satisfied by
// a second run that rewrote everything from scratch, which is the
// behaviour the idempotency rule exists to forbid.
func TestAcceptInitScenarioC(t *testing.T) {
	e := newEnv(t)

	if out, code := e.run(t, initArgs...); code != 0 {
		t.Fatalf("first run exited %d:\n%s", code, out)
	}

	const sentinel = "\n<!-- a line the operator added by hand -->\n"
	before := mustRead(t, e.instructionFile())
	mustWrite(t, e.instructionFile(), before+sentinel)

	out, code := e.run(t, initArgs...)
	if code != 0 && code != 3 {
		t.Fatalf("second run exited %d, want 0 (converged) or 3 (work reported):\n%s", code, out)
	}

	after := mustRead(t, e.instructionFile())
	if !strings.Contains(after, strings.TrimSpace(sentinel)) {
		t.Errorf("the second run destroyed a hand-edited instruction file.\n--- after ---\n%s", after)
	}
	if row := e.harnessRow(t, "claude"); !row.Detected || !row.CascadeRegistered {
		t.Errorf("the harness regressed across a second run: %+v", row)
	}
}

// TestAcceptInitOnAMachineWithNoHarness states the OTHER honest outcome:
// with no harness installed, setup completes and wires nothing, and says
// so. It must not report a harness wired, and it must not fail.
func TestAcceptInitOnAMachineWithNoHarness(t *testing.T) {
	e := newEnv(t)
	removeHarness(t, e)

	out, code := e.run(t, initArgs...)
	if code != 0 {
		t.Fatalf("cascade init --yes exited %d on a machine with no harness:\n%s", code, out)
	}
	requireContains(t, out, "not installed", "the run states why it wired nothing")
	if exists(e.hookPack()) {
		t.Errorf("a hook pack was installed for a harness that is not present")
	}
}

// contains reports whether names holds want.
func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
