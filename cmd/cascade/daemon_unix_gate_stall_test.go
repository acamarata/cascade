//go:build !windows

// Purpose: the completion gate records each denial once and the daemon's
// stall detector carries the gate-stall actions over the composed daemon's
// jobs store, Fleet and Reserver.
// Constraints: real SQLite under t.TempDir(), HOME in a fresh temp dir.
// SPORT: cmd/cascade daemon gate-stall actions (tests, P1-PBD-04d).
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// gateRows counts the recorded gate attempts and sums their attempt numbers.
func gateRows(t *testing.T, w *daemonWiring) (rows, attempts int) {
	t.Helper()
	err := w.Deps.Fleet.JobsDB().QueryRow(`SELECT COUNT(*), COALESCE(SUM(attempts), 0) FROM jobs_gate_attempts`).Scan(&rows, &attempts)
	if err != nil {
		t.Fatalf("read gate attempts: %v", err)
	}
	return rows, attempts
}

// denyOnce dispatches one completion check that must deny.
func denyOnce(t *testing.T, w *daemonWiring, id string) {
	t.Helper()
	if resp := hookDispatch(t, w, id); !resp.Deny {
		t.Fatalf("completion check allowed %s: %+v", id, resp)
	}
}

func TestCompletionGateRecordsEachDenialOnce(t *testing.T) {
	w := hookDaemon(t)
	hookEvidence(t, w, "complete")
	denyOnce(t, w, "complete")
	if rows, attempts := gateRows(t, w); rows != 1 || attempts != 1 {
		t.Fatalf("after one denial: rows %d attempts %d, want 1 and 1", rows, attempts)
	}
	g := &completionPolicyGate{store: w.Deps.Fleet.Jobs(), checkpoints: &testCompletionCheckpoints{}}
	if _, _, err := g.CompletionCheck(context.Background(), "no-such-job"); err == nil {
		t.Fatal("unknown job did not error")
	}
	if rows, attempts := gateRows(t, w); rows != 1 || attempts != 1 {
		t.Fatalf("an error return recorded a denial: rows %d attempts %d", rows, attempts)
	}
	denyOnce(t, w, "complete")
	denyOnce(t, w, "complete")
	acts := gateStallActions(w.Deps.Fleet, w.Clock)
	d, err := acts.LastDenial(context.Background(), "complete")
	if err != nil || d.Attempt != 3 || !strings.Contains(d.Reason, "completion gate") {
		t.Fatalf("LastDenial = %+v, %v; want attempt 3", d, err)
	}
	if err := acts.FailJob(context.Background(), "complete", d); err != nil {
		t.Fatalf("FailJob at 3: %v", err)
	}
	if got := hookStored(t, w, "complete"); got.State != jobs.JobStateFailed {
		t.Fatalf("job state %s, want failed", got.State)
	}
	err = acts.FailJob(context.Background(), "complete", supervision.GateDenial{Attempt: 3, Reason: d.Reason})
	if err == nil || !cascade.HasKind(err, cascade.KindConflict) && !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("second FailJob on a failed job = %v, want a refusal", err)
	}
}

func TestDaemonStallDetectorWiresGateStallActions(t *testing.T) {
	w := hookDaemon(t)
	hookEvidence(t, w, "complete")
	for range 3 {
		denyOnce(t, w, "complete")
	}
	parts, err := buildStall(w)
	if err != nil {
		t.Fatalf("buildStall: %v", err)
	}
	if err := sessions.New(w.Store, w.Clock, w.Bus).Upsert(context.Background(), sessions.SessionRecord{SessionID: "sess-1", State: "active"}); err != nil {
		t.Fatalf("Upsert session: %v", err)
	}
	at := w.Clock.Now().UnixMilli()
	for i := range 3 {
		sig := supervision.StallSignal{Kind: supervision.SignalGateDenied, SessionID: "sess-1", JobID: "complete", At: at + int64(i)}
		if err := parts.detector.Observe(context.Background(), sig); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	kind := supervision.KindStall
	items, err := parts.attention.ListInScopes(context.Background(),
		[]supervision.ScopeRef{{Kind: scope.ScopeKindSession, ID: "sess-1"}}, supervision.Filter{KindFilter: &kind, IncludeAcked: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "gate-stall:complete attempt 3/3; next retry not before "
	found := false
	for _, it := range items {
		found = found || strings.HasPrefix(it.SourceRef, want)
	}
	if !found {
		t.Fatalf("no wired gate-stall item in %+v", items)
	}
	if got := hookStored(t, w, "complete"); got.State != jobs.JobStateFailed {
		t.Fatalf("stalled job state %s, want failed", got.State)
	}
}

func TestGateStallActionsUnsetRefuses(t *testing.T) {
	w := hookDaemon(t)
	hookEvidence(t, w, "complete")
	denyOnce(t, w, "complete")
	denyOnce(t, w, "complete")
	acts := gateStallActions(w.Deps.Fleet, w.Clock)
	if acts.LastDenial == nil || acts.FailJob == nil || acts.Releases == nil || acts.RetryHint == nil {
		t.Fatalf("actions not all set: %+v", acts)
	}
	d, err := acts.LastDenial(context.Background(), "complete")
	if err != nil || d.Attempt != 2 {
		t.Fatalf("LastDenial = %+v, %v; want attempt 2", d, err)
	}
	err = acts.FailJob(context.Background(), "complete", d)
	if err == nil || !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("FailJob at attempt 2 = %v, want KindConflict", err)
	}
	if got := hookStored(t, w, "complete"); got.State != jobs.JobStateReviewing {
		t.Fatalf("job state %s, want reviewing", got.State)
	}
	if hint, err := acts.RetryHint(2); err != nil || hint != 15*time.Minute {
		t.Fatalf("RetryHint(2) = %v, %v", hint, err)
	}
	if _, err := acts.LastDenial(context.Background(), "no-such-job"); err == nil {
		t.Fatal("LastDenial of a job with no denial returned no error")
	}
}
