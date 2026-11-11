package supervision_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestGateDeniedEscalation_Integration proves the real gate's counting window.
func TestGateDeniedEscalation_Integration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		times   []time.Duration
		stalled bool
	}{
		{"three within window", []time.Duration{0, 5 * time.Minute, 10 * time.Minute}, true},
		{"two denials", []time.Duration{0, 5 * time.Minute}, false},
		{"three outside window", []time.Duration{0, 16 * time.Minute, 32 * time.Minute}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newDenialRig(t)
			for _, at := range tc.times {
				r.deny("J1", "S1", at)
				r.barrier()
			}
			items := r.items("S1")
			entries := r.entries([]journal.Kind{journal.KindEscalation})
			if !tc.stalled {
				if len(items) != 0 || len(entries) != 0 {
					t.Fatalf("premature escalation: items=%+v entries=%+v", items, entries)
				}
				r.assertCalls([5]int{})
				return
			}
			r.assertStall()
			if len(entries) == 0 {
				t.Fatal("no S1 escalation journal entry")
			}
			r.assertCalls([5]int{1, 1, 1, 1, 1})
		})
	}
}

// TestStalledJobReleasesThroughCancelInFlight proves terminal and scope cleanup.
func TestStalledJobReleasesThroughCancelInFlight(t *testing.T) {
	r := newDenialRig(t)
	got, err := r.leases.Acquire(r.ctx, "repo", "src/**", "J1")
	denialCheck(t, err)
	if !got.Granted {
		t.Fatal("subject lease not granted")
	}
	r.assertContender(false)
	r.threeDenials()
	r.assertStall()
	r.assertResult()
	job, ok, err := r.store.GetJob(r.ctx, "J1")
	denialCheck(t, err)
	if !ok || job.State != jobs.JobStateFailed {
		t.Fatalf("job = %+v, exists=%v", job, ok)
	}
	reservation, ok, err := r.reservations.GetByExecution(r.ctx, "exec-J1")
	denialCheck(t, err)
	if !ok || reservation.State != economics.ReservationReleased {
		t.Fatalf("reservation = %+v, exists=%v", reservation, ok)
	}
	held, err := r.store.LeasesForHolder(r.ctx, "J1")
	denialCheck(t, err)
	if len(held) != 1 || held[0].State != jobs.LeaseReleased {
		t.Fatalf("terminal hook did not release lease: %+v", held)
	}
	r.assertContender(true)
	r.assertCalls([5]int{1, 1, 1, 1, 1})
	events, err := r.bus.Replay(r.ctx, "jobs.gate", 0)
	denialCheck(t, err)
	for i := len(events) - 1; i >= 0; i-- {
		if strings.Contains(string(events[i].Payload), `"job_id":"J1"`) {
			ev := events[i]
			_, err = r.bus.Publish(r.ctx, "jobs.gate", ev.Kind, ev.Source, ev.Payload)
			denialCheck(t, err)
			r.barrier()
			r.assertCalls([5]int{1, 1, 1, 1, 2})
			r.assertStall()
			return
		}
	}
	t.Fatal("no third gate denial to republish")
}

// TestGateDenialReasonNeverInDirective keeps detail on the human error surface.
func TestGateDenialReasonNeverInDirective(t *testing.T) {
	r := newDenialRig(t)
	r.threeDenials()
	r.assertStall()
	r.assertResult()
	r.mu.Lock()
	reason := r.last["J1"].Reason
	r.mu.Unlock()
	directives, err := r.dirs.Drain(r.ctx, "S1")
	denialCheck(t, err)
	if len(directives) == 0 {
		t.Fatal("no real ladder directive to inspect")
	}
	for _, d := range directives {
		if strings.Contains(d.Text, reason) {
			t.Fatalf("denial leaked into directive: %q", d.Text)
		}
	}
	entries := r.entries(nil)
	if len(entries) == 0 {
		t.Fatal("no S1 journal entries to inspect")
	}
	encoded, err := json.Marshal(reason)
	denialCheck(t, err)
	for _, entry := range entries {
		if strings.Contains(string(entry.Payload), reason) || strings.Contains(string(entry.Payload), string(encoded[1:len(encoded)-1])) {
			t.Fatalf("denial leaked into journal: %s", entry.Payload)
		}
	}
}

func (r *denialRig) threeDenials() {
	for _, at := range []time.Duration{0, 5 * time.Minute, 10 * time.Minute} {
		r.deny("J1", "S1", at)
		r.barrier()
	}
}

func (r *denialRig) assertCalls(want [5]int) {
	r.t.Helper()
	r.mu.Lock()
	got := r.calls["J1"]
	r.mu.Unlock()
	if got != want {
		r.t.Fatalf("J1 calls [FailJob Releases release RetryHint LastDenial] = %v, want %v", got, want)
	}
}

func (r *denialRig) assertStall() {
	r.t.Helper()
	r.mu.Lock()
	d := r.last["J1"]
	r.mu.Unlock()
	if d.Attempt != 3 || !d.DeniedAt.Equal(denialT0.Add(10*time.Minute)) || d.Reason == "" {
		r.t.Fatalf("third denial = %+v, want attempt 3 at T0+10m with a reason", d)
	}
	items := r.items("S1")
	if len(items) != 1 {
		r.t.Fatalf("S1 items = %+v, want one", items)
	}
	it := items[0]
	if it.Kind != supervision.KindStall || it.ScopeRef != (supervision.ScopeRef{Kind: "session", ID: "S1"}) || it.Priority != 1 || it.SourceRef != r.source("J1") {
		r.t.Fatalf("gate stall item = %+v, want source %q", it, r.source("J1"))
	}
}

func (r *denialRig) entries(kinds []journal.Kind) []journal.Entry {
	entries, err := r.jr.Replay(r.ctx, "S1", journal.Cursor{}, kinds)
	denialCheck(r.t, err)
	return entries
}

func (r *denialRig) assertResult() {
	r.t.Helper()
	var kind, message string
	denialCheck(r.t, r.db.QueryRowContext(r.ctx, `SELECT error_kind, error_message FROM jobs_execution_result WHERE execution_id = ?`, "exec-J1").Scan(&kind, &message))
	r.mu.Lock()
	reason := r.last["J1"].Reason
	r.mu.Unlock()
	if reason == "" || kind != cascade.KindPolicyDenied.String() || message != "gate_attempts_exhausted: "+reason {
		r.t.Fatalf("execution result = (%q,%q), want policy denial and exact reason %q", kind, message, reason)
	}
}

func (r *denialRig) assertContender(want bool) {
	r.t.Helper()
	got, err := r.leases.Acquire(r.ctx, "repo", "src/**", "contender")
	denialCheck(r.t, err)
	if got.Granted != want {
		r.t.Fatalf("contender Granted = %v, want %v", got.Granted, want)
	}
}
