//go:build !windows && integration

// Purpose: TestDaemonResumeProducerCrashSites: a child daemon SIGKILLed by
//
//	its store seam after the cursor, after the request record, after the
//	last leg done and after the final marker (before its deletes) is
//	restarted: zero provider calls at start, status.get counts the
//	resumable ones, a re-attach of each resumable case delivers, and left
//	alone each is expired by the startup sweep once the ttl has elapsed,
//	leaving zero orphan records.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/resume"
)

// crashSite is one producer crash site and what a restart must find.
type crashSite struct {
	site      string
	pending   string // status.get fanout-resume detail after the restart
	reattach  bool   // resumable: a re-attach delivers
	calls     int32  // provider calls the re-attach makes
	scanFinal string // marker the scan leaves on a non-resumable case
}

// sitePrompt is the prompt of site's fan-out (no colon: --input reads
// "role:content").
func sitePrompt(site string) string { return "crash site " + strings.ReplaceAll(site, ":", " ") }

// crashAtSite runs a 2-leg fan-out on a child daemon that SIGKILLs itself
// at site, and returns the request id and the provider calls so far.
func crashAtSite(t *testing.T, h *resumeHome, site string) (string, int32) {
	t.Helper()
	res := h.crashRun(t, h.start(t, childOpts{site: site}), "2", sitePrompt(site))
	return res.requestID(t), h.prov.calls.Load()
}

func TestDaemonResumeProducerCrashSites(t *testing.T) {
	sites := []crashSite{
		{site: "cursor", pending: "pending 0", scanFinal: resume.OutcomeTerminal},
		{site: "request", pending: "pending 1", reattach: true, calls: 2},
		{site: "done:2", pending: "pending 1", reattach: true, calls: 0},
		{site: "marker", pending: "pending 0", scanFinal: resume.OutcomeDelivered},
	}
	for _, s := range sites {
		h := newResumeHome(t, false, "1h", "1h")
		id, calls := crashAtSite(t, h, s.site)
		c := h.start(t, childOpts{})
		if d := h.resumeDetail(t); d != s.pending || h.prov.calls.Load() != calls {
			t.Fatalf("%s: restart reports %q with provider calls %d -> %d; want %q and zero calls at start", s.site, d, calls, h.prov.calls.Load(), s.pending)
		}
		final := s.scanFinal
		if s.reattach {
			res := h.runFan(t, "2", id, sitePrompt(s.site))
			if res.err != nil || res.outputs() != 2 || h.prov.calls.Load()-calls != s.calls {
				t.Fatalf("%s: re-attach err %v, outputs %d, calls %d; want 2 outputs and %d calls", s.site, res.err, res.outputs(), h.prov.calls.Load()-calls, s.calls)
			}
			final = resume.OutcomeDelivered
		}
		c.term(t)
		if st, counts := h.inspect(t, id); st.Final != final || records(counts) != 0 {
			t.Fatalf("%s: marker %q, records %v; want %q and zero records", s.site, st.Final, counts, final)
		}
	}
	for _, s := range sites[1:3] { // the resumable cases, never re-attached
		h := newResumeHome(t, false, "1s", "100ms")
		id, calls := crashAtSite(t, h, s.site)
		time.Sleep(1500 * time.Millisecond) // the ttl elapses while the daemon is down
		c := h.start(t, childOpts{})
		time.Sleep(500 * time.Millisecond) // the startup sweep runs
		c.term(t)
		if st, counts := h.inspect(t, id); st.Final != resume.OutcomeExpired || records(counts) != 0 || h.prov.calls.Load() != calls {
			t.Fatalf("%s left alone: marker %q, records %v, calls %d -> %d; want expired, zero records, zero calls",
				s.site, st.Final, counts, calls, h.prov.calls.Load())
		}
	}
}
