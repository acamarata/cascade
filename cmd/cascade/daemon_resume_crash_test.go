//go:build !windows && integration

// Purpose: P1-CORE-15 acceptance on a child daemon (EPIC Decision 12): a
//
//	SIGKILLed fan-out is recovered by the client re-attach with the stored
//	leg replayed and the missing legs dispatched once; a restart without a
//	re-attach makes zero provider calls and the sweep expires the task at
//	fanout_record_ttl; kills during the call and two re-attaches cap the
//	unfinished legs at three starts and the third re-attach dispatches
//	nothing and finalizes unknown_outcome.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/resume"
)

const attemptsNS = "conductor.fanout.attempts"

// crashMidFanOut starts a child daemon, runs a 3-leg fan-out whose first
// leg is answered and whose other two are held at the provider, and
// SIGKILLs the daemon once leg one's ok done is committed and all three
// legs have started. It returns the request id the run printed.
func crashMidFanOut(t *testing.T, h *resumeHome, o childOpts, prompt string) string {
	t.Helper()
	h.prov.free.Store(1)
	o.site, o.notify = "done:1", filepath.Join(h.root, "leg-done")
	c := h.start(t, o)
	run := h.asyncFan(t, "3", "", prompt)
	waitFile(t, o.notify)
	for i := 0; i < 3; i++ {
		waitEntered(t, h.prov)
	}
	c.kill9(t)
	res := <-run
	if res.err == nil {
		t.Fatal("the run survived its daemon's SIGKILL")
	}
	return res.requestID(t)
}

// drainEntered empties the provider's entered queue.
func drainEntered(p *recordedProvider) {
	for {
		select {
		case <-p.entered:
		default:
			return
		}
	}
}

func TestDaemonReattachAfterSIGKILL(t *testing.T) {
	h := newResumeHome(t, true, "1h", "1h")
	id := crashMidFanOut(t, h, childOpts{}, "re-attach prompt")
	st, counts := h.inspect(t, id)
	if len(st.Completed) != 1 || st.Final != "" || counts["conductor.fanout.requests"] != 1 || counts["conductor.fanout.legs"] != 1 {
		t.Fatalf("after SIGKILL: state %+v, records %v; want one ok leg, no marker, the request and one leg result kept", st, counts)
	}
	var done int
	for leg := range st.Completed {
		done = leg
	}
	c := h.start(t, childOpts{})
	h.prov.release()
	before := h.prov.calls.Load()
	res := h.runFan(t, "3", id, "re-attach prompt")
	if res.err != nil || res.outputs() != 3 || h.prov.calls.Load()-before != 2 {
		t.Fatalf("re-attach: err %v, outputs %d, provider calls %d; want 3 outputs and the two missing legs dispatched once each\n%s",
			res.err, res.outputs(), h.prov.calls.Load()-before, res.stdout)
	}
	c.term(t)
	st, counts = h.inspect(t, id)
	if st.Final != resume.OutcomeDelivered || records(counts) != 0 || counts[attemptsNS] != 0 || st.Starts[done] != 1 {
		t.Fatalf("after re-attach: marker %q, records %v, leg %d starts %d; want delivered, zero records and slots, the stored leg never restarted",
			st.Final, counts, done, st.Starts[done])
	}
}

func TestDaemonResumeNoBackgroundDispatch(t *testing.T) {
	const ttl = 3 * time.Second
	h := newResumeHome(t, true, ttl.String(), "100ms")
	id := crashMidFanOut(t, h, childOpts{}, "unattached prompt")
	crashed := time.Now()
	c := h.start(t, childOpts{})
	h.prov.release() // a background dispatch would now be answered and counted
	calls := h.prov.calls.Load()
	if d := h.resumeDetail(t); d != "pending 1" {
		t.Fatalf("status.get fanout-resume = %q, want \"pending 1\"", d)
	}
	time.Sleep(time.Until(crashed.Add(ttl + 2*time.Second)))
	c.term(t)
	st, counts := h.inspect(t, id)
	if st.Final != resume.OutcomeExpired || records(counts) != 0 || h.prov.calls.Load() != calls {
		t.Fatalf("after the ttl: marker %q, records %v, provider calls %d -> %d; want expired, zero records, zero calls",
			st.Final, counts, calls, h.prov.calls.Load())
	}
}

func TestDaemonResumeCrashLoopCapsAttempts(t *testing.T) {
	h := newResumeHome(t, true, "1h", "1h")
	id := crashMidFanOut(t, h, childOpts{}, "loop prompt")
	st, _ := h.inspect(t, id)
	var open []int
	for leg := 0; leg < 3; leg++ {
		if _, ok := st.Completed[leg]; !ok {
			open = append(open, leg)
		}
	}
	for round := 1; round <= 2; round++ { // the daemon dies during two re-attaches
		c := h.start(t, childOpts{})
		drainEntered(h.prov)
		run := h.asyncFan(t, "3", id, "loop prompt")
		for range open {
			waitEntered(t, h.prov)
		}
		c.kill9(t)
		if res := <-run; res.err == nil {
			t.Fatalf("re-attach %d survived its daemon's SIGKILL", round)
		}
	}
	st, _ = h.inspect(t, id)
	for _, leg := range open {
		if st.Starts[leg] != resume.MaxLegStarts {
			t.Fatalf("leg %d has %d starts after the crash loop, want %d", leg, st.Starts[leg], resume.MaxLegStarts)
		}
	}
	c := h.start(t, childOpts{})
	h.prov.release()
	calls := h.prov.calls.Load()
	res := h.runFan(t, "3", id, "loop prompt")
	if res.err == nil || !strings.Contains(res.err.Error(), "start cap") || h.prov.calls.Load() != calls || res.outputs() != 0 {
		t.Fatalf("third re-attach: err %v, provider calls %d -> %d; want the start-cap refusal and zero calls", res.err, calls, h.prov.calls.Load())
	}
	c.term(t)
	st, counts := h.inspect(t, id)
	if st.Final != resume.OutcomeUnknown || records(counts) != 0 {
		t.Fatalf("after the capped re-attach: marker %q, records %v; want unknown_outcome and zero records", st.Final, counts)
	}
}
