// Purpose: the fleet adapter of contract:fanout-leg-results against the
//   real SQLite driver and the real conductor.FanOut: no Response bytes in
//   journal payloads, DeleteTask scope, per-leg attempts and the start cap,
//   create-only records, record integrity, done validation, outcome-aware
//   classification, and the crash window replayed through this adapter.
// Constraints: t.TempDir only; offline.
// SPORT: internal.fleet.resume.ResumeManager/CHANGE (tests) (P1-CORE-18).

package resume

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const secretOutput = "model-output-" + "must-never-reach-the-journal"

func legReq() provider.ModelRequest {
	return provider.ModelRequest{TaskID: "client-task", TaskClass: "chat", Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
}

func allowAll(context.Context, provider.ModelRequest) error { return nil }

func permitAll(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func countingExec(calls *int32) func(context.Context, provider.ModelRequest) (provider.ModelResponse, error) {
	return func(context.Context, provider.ModelRequest) (provider.ModelResponse, error) {
		atomic.AddInt32(calls, 1)
		return provider.ModelResponse{JobID: "job-1", Output: secretOutput, Usage: provider.Usage{InputTokens: 3, OutputTokens: 7}}, nil
	}
}

func newAdapter(t *testing.T) (*journalAppenderAdapter, journal.Store, provider.Store) {
	t.Helper()
	js, raw, _ := newRealStore(t)
	a, err := newLegAdapter(js, raw)
	if err != nil {
		t.Fatalf("newLegAdapter: %v", err)
	}
	return a, js, raw
}

func TestLegResultNotInJournalPayload(t *testing.T) {
	a, js, _ := newAdapter(t)
	var calls int32
	if _, err := conductor.FanOut(context.Background(), "fo-p", legReq(), 2, nil, permitAll, a, a, allowAll, countingExec(&calls)); err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	entries, err := js.Replay(context.Background(), FanOutEntity("fo-p"), journal.Cursor{EntityID: FanOutEntity("fo-p")}, nil)
	if err != nil || len(entries) != 4 {
		t.Fatalf("journal entries = %d (%v), want 4 (start+done per leg)", len(entries), err)
	}
	for _, e := range entries {
		if strings.Contains(string(e.Payload), secretOutput) {
			t.Fatalf("journal payload carries Response content: %s", e.Payload)
		}
		var p legPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.RequestDigest == "" {
			t.Fatalf("payload %s: want a request_digest (%v)", e.Payload, err)
		}
		rec, _, _ := a.GetLegResult(context.Background(), "fo-p", p.LegIndex)
		if e.Kind == journal.KindFanOutLegDone && (p.Outcome != conductor.LegOutcomeOK || p.ResultKey != conductor.LegResultKey("fo-p", p.LegIndex) || p.RequestDigest != rec.RequestDigest) {
			t.Fatalf("done payload %s: want outcome ok, result_key and the record's digest %q", e.Payload, rec.RequestDigest)
		}
		if want := "fo-p#" + itoa(uint64(p.LegIndex)) + "#1#" + string(e.Kind); e.OperationID != want {
			t.Fatalf("operation id = %q, want %q", e.OperationID, want)
		}
	}
	if rec, ok, _ := a.GetLegResult(context.Background(), "fo-p", 1); !ok || rec.Response.Output != secretOutput || rec.TaskID != "client-task" {
		t.Fatalf("record = %+v (found %v), want the output stored with the client TaskID", rec, ok)
	}
}

func TestDeleteTaskRemovesRecords(t *testing.T) {
	a, _, raw := newAdapter(t)
	ctx := context.Background()
	for _, k := range []struct {
		id  string
		leg int
	}{{"fo-a", 0}, {"fo-a", 1}, {"fo-a", 10}, {"fo-b", 0}, {"fo-ab", 0}} {
		if err := a.PutLegResult(ctx, conductor.LegResult{FanOutID: k.id, LegIndex: k.leg, RequestDigest: "d"}); err != nil {
			t.Fatalf("Put %s#%d: %v", k.id, k.leg, err)
		}
	}
	if err := raw.Put(ctx, legResultsNamespace, "fo-a#x#0", []byte(`{}`)); err != nil { // a key no leg of fo-a can own
		t.Fatalf("seed foreign key: %v", err)
	}
	if err := a.DeleteTask(ctx, "fo-a"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	for key, want := range map[string]bool{"fo-a#0": false, "fo-a#1": false, "fo-a#10": false, "fo-b#0": true, "fo-ab#0": true, "fo-a#x#0": true} {
		_, err := raw.Get(ctx, legResultsNamespace, key)
		if got := err == nil; got != want {
			t.Errorf("after DeleteTask(fo-a): %s present = %v, want %v (%v)", key, got, want, err)
		}
	}
	if err := a.DeleteTask(ctx, "fo#"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("DeleteTask with a separator = %v, want KindInvalidInput", err)
	}
}

func TestLegAdapterAttemptCap(t *testing.T) {
	a, js, _ := newAdapter(t)
	ctx := context.Background()
	for want := uint64(1); want <= maxLegStarts; want++ {
		got, err := a.AppendLeg(ctx, "fanout_leg_started", "fo-c", 0, map[string]string{"request_digest": "d"})
		if err != nil || got != want {
			t.Fatalf("start %d = (%d, %v), want attempt %d", want, got, err, want)
		}
	}
	if _, err := a.AppendLeg(ctx, "fanout_leg_started", "fo-c", 0, nil); err != ErrLegAttemptsExhausted {
		t.Fatalf("fourth start = %v, want ErrLegAttemptsExhausted", err)
	}
	if got, err := a.AppendLeg(ctx, "fanout_leg_started", "fo-c", 1, nil); err != nil || got != 1 {
		t.Fatalf("other leg's first start = (%d, %v), want 1", got, err)
	}
	entries, _ := js.Replay(ctx, FanOutEntity("fo-c"), journal.Cursor{EntityID: FanOutEntity("fo-c")}, nil)
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 (three starts of leg 0, one of leg 1; the refused start wrote nothing)", len(entries))
	}
	_, _, err := classify(append([]journal.Entry{cursorEntry(t, 2)}, entries...))
	if err != ErrLegAttemptsExhausted || !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("classify(leg 0 at the cap) = %v, want ErrLegAttemptsExhausted (unknown outcome)", err)
	}
}

func TestLegAdapterRecordIntegrity(t *testing.T) {
	a, _, raw := newAdapter(t)
	ctx := context.Background()
	rec := conductor.LegResult{FanOutID: "fo-i", LegIndex: 0, RequestDigest: "d1", Response: provider.ModelResponse{Output: "first"}}
	if err := a.PutLegResult(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	before, _ := raw.Get(ctx, legResultsNamespace, "fo-i#0")
	rec.Response.Output = "overwrite"
	if err := a.PutLegResult(ctx, rec); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second Put = %v, want KindConflict (create-only)", err)
	}
	if after, _ := raw.Get(ctx, legResultsNamespace, "fo-i#0"); string(after) != string(before) {
		t.Fatalf("record rewritten: %s -> %s", before, after)
	}
	for body, name := range map[string]string{`{"version":2}`: "fo-i#1", `not json`: "fo-i#2"} {
		if err := raw.Put(ctx, legResultsNamespace, name, []byte(body)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for _, leg := range []int{1, 2} {
		if _, ok, err := a.GetLegResult(ctx, "fo-i", leg); ok || !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Fatalf("Get(fo-i#%d) = (%v, %v), want KindIntegrity", leg, ok, err)
		}
	}
	journalOnly := &journalAppenderAdapter{}
	if err := journalOnly.PutLegResult(ctx, rec); err != ErrLegStoreUnset {
		t.Fatalf("Put without a store = %v, want ErrLegStoreUnset", err)
	}
	if _, err := newLegAdapter(nil, raw); err != ErrLegStoreUnset {
		t.Fatalf("newLegAdapter(nil journal) = %v, want ErrLegStoreUnset", err)
	}
}

func TestLegAdapterRejectsBadDone(t *testing.T) {
	a, _, _ := newAdapter(t)
	for name, f := range map[string]map[string]string{
		"no attempt":        {"outcome": "ok", "result_key": "fo-v#0"},
		"unknown outcome":   {"attempt": "1", "outcome": "maybe"},
		"ok, foreign key":   {"attempt": "1", "outcome": "ok", "result_key": "fo-w#0"},
		"failed with a key": {"attempt": "1", "outcome": "failed_retryable", "result_key": "fo-v#0"},
	} {
		if _, err := a.AppendLeg(context.Background(), "fanout_leg_done", "fo-v", 0, f); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: err = %v, want KindInvalidInput", name, err)
		}
	}
}

func cursorEntry(t *testing.T, legs int) journal.Entry {
	t.Helper()
	req, _ := json.Marshal(legReq())
	p, err := json.Marshal(resumeCursorPayload{T: "cursor", TaskID: "client-task", Legs: legs, Request: req})
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	return journal.Entry{Kind: journal.KindResumeCursor, Payload: p}
}

func doneEntry(leg int, outcome string) journal.Entry {
	p, _ := json.Marshal(legPayload{LegIndex: leg, Attempt: 1, Outcome: outcome})
	return journal.Entry{Kind: journal.KindFanOutLegDone, Payload: p}
}

func TestClassifyFanOutLegOutcomes(t *testing.T) {
	cur, _, err := classify([]journal.Entry{cursorEntry(t, 3), doneEntry(0, "ok"), doneEntry(1, "failed_retryable"), doneEntry(2, "")})
	if err != nil || cur == nil || len(cur.Completed) != 1 {
		t.Fatalf("classify = (%+v, %v), want resumable with only the ok leg completed", cur, err)
	}
	if _, ok := cur.Completed[0]; !ok {
		t.Fatalf("completed = %v, want leg 0", cur.Completed)
	}
	_, _, err = classify([]journal.Entry{cursorEntry(t, 2), doneEntry(0, "ok"), doneEntry(1, "failed_terminal")})
	if err != ErrLegTerminal {
		t.Fatalf("classify(failed_terminal) = %v, want ErrLegTerminal", err)
	}
	store, _, _ := newRealStore(t)
	for i, e := range []journal.Entry{cursorEntry(t, 1), doneEntry(0, "failed_terminal")} {
		if _, err := store.Append(context.Background(), FanOutEntity("fo-t"), e.Kind, "op-"+itoa(uint64(i)), e.Payload); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var calls []fakeFanOutCall
	mgr, _ := New(store, fakeFanOut(&calls, nil, nil), nil, nil, nil, nil, "darwin")
	report, err := mgr.Run(context.Background())
	if err != nil || len(report.Outcomes) != 1 || report.Outcomes[0].Classification != ClassTerminal || len(calls) != 0 {
		t.Fatalf("Run = (%+v, %v), calls = %d; want one terminal outcome and no dispatch", report, err, len(calls))
	}
}

// failDoneStore fails every KindFanOutLegDone append once armed: the crash
// window between PutLegResult and the done append.
type failDoneStore struct {
	journal.Store
	armed bool
}

func (f *failDoneStore) Append(ctx context.Context, id string, k journal.Kind, op string, p json.RawMessage) (journal.Entry, error) {
	if f.armed && k == journal.KindFanOutLegDone {
		return journal.Entry{}, cascade.New(cascade.KindUnavailable, "injected: process died before the done append")
	}
	return f.Store.Append(ctx, id, k, op, p)
}

func TestLegAdapterCrashWindowReplays(t *testing.T) {
	js, raw, _ := newRealStore(t)
	crashing, _ := newLegAdapter(&failDoneStore{Store: js, armed: true}, raw)
	var calls int32
	if _, err := conductor.FanOut(context.Background(), "fo-w", legReq(), 1, nil, permitAll, crashing, crashing, allowAll, countingExec(&calls)); err == nil {
		t.Fatal("run 1: want the injected done-append error returned")
	}
	resumed, _ := newLegAdapter(js, raw)
	out, err := conductor.FanOut(context.Background(), "fo-w", legReq(), 1, nil, permitAll, resumed, resumed, allowAll, countingExec(&calls))
	if err != nil || calls != 1 || out[0].Output != secretOutput {
		t.Fatalf("run 2 = (%v, %v), provider calls = %d; want the stored output with zero new calls", out, err, calls)
	}
	entries, _ := js.Replay(context.Background(), FanOutEntity("fo-w"), journal.Cursor{EntityID: FanOutEntity("fo-w")}, nil)
	completed, _, _ := completedLegs(entries)
	if _, ok := completed[0]; !ok || len(entries) != 2 {
		t.Fatalf("entries = %d, completed = %v; want one start and the appended ok done", len(entries), completed)
	}
}
