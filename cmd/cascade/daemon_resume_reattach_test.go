//go:build !windows && integration

// Purpose: P1-CORE-15 acceptance on a child daemon: a policy flipped to
//
//	deny between the crash and the re-attach refuses the re-attach with
//	zero stored bytes returned and zero provider calls, on the dispatch
//	path (legs still to run) and on the replay path (every leg stored);
//	SIGTERM mid fan-out finalizes it cancelled with zero records and the
//	next start classifies nothing for it.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/resume"
)

// denyReattach flips the child's flag-file policy to "allow the parent
// door, deny every leg", restarts the daemon and re-attaches id, then
// checks the refusal: policy-denied, no output, zero provider calls, the
// fan-out finalized terminal with zero records.
func denyReattach(t *testing.T, h *resumeHome, flag, id, prompt, path string) {
	t.Helper()
	if err := os.WriteFile(flag, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := h.start(t, childOpts{policy: flag})
	h.prov.release() // a leg that slipped past the door would be answered and counted
	calls := h.prov.calls.Load()
	res := h.runFan(t, "3", id, prompt)
	if res.err == nil || !strings.Contains(res.err.Error(), "policy now denies") || res.outputs() != 0 || h.prov.calls.Load() != calls {
		t.Fatalf("%s path: re-attach err %v, outputs %d, provider calls %d -> %d; want the policy refusal, no output and zero calls\n%s",
			path, res.err, res.outputs(), calls, h.prov.calls.Load(), res.stdout)
	}
	c.term(t)
	if st, counts := h.inspect(t, id); st.Final != resume.OutcomeTerminal || records(counts) != 0 {
		t.Fatalf("%s path: marker %q, records %v; want terminal and zero records", path, st.Final, counts)
	}
}

func TestDaemonReattachRefusesNowDeniedLeg(t *testing.T) {
	// Dispatch path: one leg stored, two still to run.
	h := newResumeHome(t, true, "1h", "1h")
	flag := filepath.Join(h.root, "deny")
	id := crashMidFanOut(t, h, childOpts{policy: flag}, "deny prompt")
	denyReattach(t, h, flag, id, "deny prompt", "dispatch")

	// Replay path: every leg stored, the daemon killed before delivery.
	h = newResumeHome(t, false, "1h", "1h")
	flag = filepath.Join(h.root, "deny")
	res := h.crashRun(t, h.start(t, childOpts{policy: flag, site: "done:3"}), "3", "replay prompt")
	id = res.requestID(t)
	if st, counts := h.inspect(t, id); len(st.Completed) != 3 || counts["conductor.fanout.legs"] != 3 {
		t.Fatalf("seed: completed %v, records %v; want three stored legs", st.Completed, counts)
	}
	denyReattach(t, h, flag, id, "replay prompt", "replay")
}

func TestDaemonShutdownFinalizesInFlight(t *testing.T) {
	h := newResumeHome(t, true, "1h", "1h") // every provider call is held
	c := h.start(t, childOpts{})
	run := h.asyncFan(t, "3", "", "shutdown prompt")
	for i := 0; i < 3; i++ {
		waitEntered(t, h.prov)
	}
	c.term(t)
	res := <-run
	if res.err == nil {
		t.Fatal("the run survived its daemon's shutdown")
	}
	id := res.requestID(t)
	st, counts := h.inspect(t, id)
	if st.Final != resume.OutcomeCancelled || records(counts) != 0 {
		t.Fatalf("after SIGTERM: marker %q, records %v; want cancelled and zero records", st.Final, counts)
	}
	c = h.start(t, childOpts{})
	if d := h.resumeDetail(t); d != "pending 0" {
		t.Fatalf("next start reports %q, want \"pending 0\" (nothing classified for the cancelled fan-out)", d)
	}
	c.term(t)
	if again, _ := h.inspect(t, id); again.Final != resume.OutcomeCancelled || len(again.Starts) != len(st.Starts) {
		t.Fatalf("next start touched the cancelled fan-out: %+v", again)
	}
}
