// Purpose: the events and callbacks a dispatch produces, and the acceptance
// refusal of a stream-only snapshot, over the rig's real local executor.
// Split from stream_dispatch_test.go to keep both under the 300-line cap.
//
// SPORT: internal.ci.Dispatcher.Dispatch/TESTED (P1-CI-01).
package ci

import (
	"context"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestTerminalFailureLeavesOutboxUnconfirmed(t *testing.T) {
	for _, stage := range []string{"source", "publish", "effect"} {
		t.Run(stage, func(t *testing.T) {
			r := newRig(t)
			ctx := context.Background()
			e := dispatchEntry{Ref: r.ref, Snapshot: CandidateSnapshot{AttemptID: "a", CheckpointID: "checkpoint"}, Kinds: []RequirementKind{RequirementUnit}}
			if err := r.d.ensureIntents(ctx, e); err != nil {
				t.Fatal(err)
			}
			message := refuseTerminalStage(t, r, stage)
			called := 0
			r.d.OnTerminal(func(CIResultEvent) { called++ })
			_, err := r.d.recordTerminal(ctx, e, RequirementUnit, outboxKeyFor(e, RequirementUnit), SubJobResult{RunID: -1, RepoID: 7, Passed: true})
			assertStreamError(t, err, cascade.KindUnavailable, message)
			if got := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox WHERE state = 'intent'`); got != 1 {
				t.Fatalf("failed terminal effect confirmed its intent: %d", got)
			}
			wantMarker, wantCallback := 1, 0
			if stage == "source" {
				wantMarker = 0
			}
			if stage == "effect" {
				wantCallback = 1
			}
			if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_run_stream`); n != wantMarker || called != wantCallback {
				t.Fatalf("stage %s: markers=%d callbacks=%d, want %d/%d", stage, n, called, wantMarker, wantCallback)
			}
		})
	}
}

// refuseTerminalStage fails one durable transition without replacing the stores.
func refuseTerminalStage(t *testing.T, r *streamRig, stage string) string {
	t.Helper()
	var err error
	message := ""
	switch stage {
	case "source":
		_, err = r.ciDB.Exec(`CREATE TRIGGER refuse_source BEFORE INSERT ON ci_run_source
			BEGIN SELECT RAISE(ABORT, 'source refused'); END`)
		message = "upsert run source"
	case "publish":
		err = r.bus.Close()
		message = "publish event"
	case "effect":
		_, err = r.jobsDB.Exec(`CREATE TRIGGER refuse_effect BEFORE UPDATE ON jobs_outbox
			BEGIN SELECT RAISE(ABORT, 'effect refused'); END`)
		message = "effect refused"
	}
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestCheckpointEventsPublished(t *testing.T) {
	r := newRig(t)
	r.useLocal(&lockedExec{}, nil)
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	for kind, ek := range map[string]events.EventKind{"dispatched": EventKindCheckpointDispatched, "terminal": EventKindCheckpointTerminal} {
		evs := r.streamEvents(ek)
		if len(evs) != 2 {
			t.Fatalf("%s events = %d, want one per kind (2)", kind, len(evs))
		}
		kinds := map[string]bool{}
		for _, p := range evs {
			kinds[p.Kind] = true
			if p.JobID != "job-1" || p.CheckpointID != snap.CheckpointID || p.TreeHash != snap.TreeHash ||
				p.Acceptance || !p.ViaStream || p.Sensitivity != "internal" || p.ExecutorKind != kindLocalIf(kind) ||
				(kind == "terminal" && (p.RunID == 0 || p.RepoID == 0 || !p.Passed)) {
				t.Fatalf("%s payload = %+v, want the checkpoint's identity with via_stream true", kind, p)
			}
		}
		if !kinds["format"] || !kinds["lint"] || len(kinds) != 2 {
			t.Fatalf("%s kinds = %v, want format and lint", kind, kinds)
		}
	}
}

// kindLocalIf is the executor kind a payload must carry: a dispatched event
// precedes any executor, so it carries none.
func kindLocalIf(kind string) string {
	if kind == "terminal" {
		return "local"
	}
	return ""
}

func TestDispatcherOnTerminalInvoked(t *testing.T) {
	r := newRig(t)
	r.useLocal(&lockedExec{}, nil)
	var mu sync.Mutex
	var got []CIResultEvent
	committed := map[string]string{}
	r.d.OnTerminal(func(ev CIResultEvent) {
		var status string
		_ = r.ciDB.QueryRow(`SELECT status FROM ci_run WHERE run_id = ? AND repo_id = ?`, ev.RunID, ev.RepoID).Scan(&status)
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
		committed[string(ev.Kind)] = status
	})
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("OnTerminal fired %d times, want once per terminal sub-job (2)", len(got))
	}
	for _, ev := range got {
		if committed[string(ev.Kind)] != string(RunStatusCompleted) {
			t.Fatalf("kind %s: ci_run status at callback time = %q, want the row already committed", ev.Kind, committed[string(ev.Kind)])
		}
		if ev.CheckpointID != snap.CheckpointID || ev.TreeHash != snap.TreeHash || !ev.Passed || ev.Acceptance {
			t.Fatalf("event = %+v, want the checkpoint's identity", ev)
		}
	}
}

func TestCIResultEventCarriesRunKey(t *testing.T) {
	r := newRig(t)
	r.useLocal(&lockedExec{}, nil)
	var mu sync.Mutex
	var got []CIResultEvent
	r.d.OnTerminal(func(ev CIResultEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	})
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no terminal events (positive control)")
	}
	for _, ev := range got {
		var name string
		if err := r.ciDB.QueryRow(`SELECT name FROM ci_run WHERE run_id = ? AND repo_id = ?`, ev.RunID, ev.RepoID).Scan(&name); err != nil {
			t.Fatalf("event %+v selects no ci_run row: %v", ev, err)
		}
		if want := "stream-" + snap.AttemptID + "-" + string(ev.Kind); name != want {
			t.Fatalf("event for kind %s selects row %q, want %q", ev.Kind, name, want)
		}
		if via, err := RunViaStream(context.Background(), r.ciDB, ev.RunID, ev.RepoID); err != nil || !via {
			t.Fatalf("RunViaStream(%d,%d) = %v, %v; want true", ev.RunID, ev.RepoID, via, err)
		}
	}
}

func TestStreamOnlySnapshotRefusedForAcceptance(t *testing.T) {
	r := newRig(t)
	r.useLocal(&lockedExec{}, nil)
	ctx := context.Background()
	r.commit(map[string]string{"docs/a.md": "a"})
	writeRepoFile(t, r.wt, "docs/notes.txt", "declared untracked")
	r.ref.Untracked = []string{"docs/notes.txt"}
	streamSnap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("stream Checkpoint over a declared untracked path: %v", err)
	}
	if !streamSnap.StreamOnly() {
		t.Fatalf("snapshot %+v must be marked stream-only", streamSnap)
	}
	_, outboxBefore, _ := r.zeroRows()
	plan := CIRequirementPlan{Requirement: CIRequirement{Format: true}, RiskClass: "low"}
	if _, err := r.d.Dispatch(ctx, r.ref, streamSnap, plan, true); !errChainHas(err, ErrTreeHashMismatch) {
		t.Fatalf("acceptance over a stream-only snapshot: err = %v, want ErrTreeHashMismatch", err)
	}
	if _, outbox, _ := r.zeroRows(); outbox != outboxBefore {
		t.Fatalf("a refused acceptance wrote outbox rows: %d -> %d", outboxBefore, outbox)
	}

	// Positive control: a commit-only checkpoint takes the acceptance dispatch.
	r.ref.Untracked = nil
	r.commit(map[string]string{"docs/b.md": "b"})
	commitOnly, err := r.checkpoint()
	if err != nil || commitOnly.StreamOnly() {
		t.Fatalf("commit-only Checkpoint = %+v, %v", commitOnly, err)
	}
	evs, err := r.d.Dispatch(ctx, r.ref, commitOnly, plan, true)
	if err != nil || len(evs) != 1 || !evs[0].Acceptance {
		t.Fatalf("acceptance over a commit-only snapshot = %+v, %v; want one acceptance result", evs, err)
	}
}
