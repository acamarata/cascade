// Purpose: the durable clear, the hold, the approval gate and the daemon
//
//	re-arm over fake backends; no real clipboard, no sleeps.
//
// SPORT: internal/secrets RearmHostPendingClears/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestClipboardFailedClearKeepsRow: a failed clear keeps the row and marks
// the clear unknown-outcome, so the re-arm can retry it.
func TestClipboardFailedClearKeepsRow(t *testing.T) {
	r := newRig()
	r.ops.clearErr = errors.New("pbcopy exit 1")
	err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Write = %v, want the KindUnavailable clear failure", err)
	}
	if r.store.len() != 1 || r.tr.count("delete") != 0 || r.tr.count("unknown:clipboard.clear") != 1 {
		t.Fatalf("rows = %d, trace = %v; want the row kept and the clear unknown-outcome", r.store.len(), r.tr.list())
	}
}

// TestClipboardSetFailureClearsAndKeepsRowUntilConfirmed: a failing set is
// unknown-outcome, is cleared at once, and the row goes only after the
// confirmed clear.
func TestClipboardSetFailureClearsAndKeepsRowUntilConfirmed(t *testing.T) {
	r := newRig()
	r.ops.setErr = errors.New("exit 1")
	if err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Write = %v, want KindUnavailable", err)
	}
	want := []string{"probe", "begin:clipboard.write", "put", "set", "unknown:clipboard.write",
		"begin:clipboard.clear", "clear", "confirm:clipboard.clear", "delete"}
	if got := r.tr.list(); !slices.Equal(got, want) {
		t.Fatalf("order = %v\nwant    %v", got, want)
	}
}

// signedFor mints a vault.get.clipboard record under a fresh key.
func signedFor(t *testing.T, clock runtime.Clock) (ed25519.PublicKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := policy.NewApprovalSigner(keySource{priv}, clock)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := signer.Sign(context.Background(), policy.ApprovalRecord{Verb: ClipboardApprovalVerb,
		ParamsDigest: "00", Requester: "cli", Origin: "cli", Risk: policy.L4, Sensitivity: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return pub, tok
}

type keySource struct{ priv ed25519.PrivateKey }

func (k keySource) ApprovalKey(context.Context) (string, ed25519.PrivateKey, error) {
	return "test", slices.Clone(k.priv), nil
}

// TestClipboardWriteRefusedWithoutApproval: a random 64-byte token and a
// record signed by a DIFFERENT key both refuse with KindElevationRequired,
// audited, and the backend is never reached; the matching record passes.
func TestClipboardWriteRefusedWithoutApproval(t *testing.T) {
	clock := runtime.NewFixedClock(clipNow)
	pub, good := signedFor(t, clock)
	_, foreign := signedFor(t, clock)
	random := make([]byte, 64)
	_, _ = rand.Read(random)
	v, err := policy.NewApprovalVerifier(pub, clock)
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string][]byte{"random": random, "foreign key": foreign} {
		r := newRig()
		err := r.writer(t, v).Write(context.Background(), tok, []byte("v"))
		if !cascade.HasKind(err, cascade.KindElevationRequired) {
			t.Fatalf("%s: Write = %v, want KindElevationRequired", name, err)
		}
		if len(r.tr.list()) != 0 || len(r.aw.events) != 1 || r.aw.events[0].Action != clipboardRefusedAction {
			t.Fatalf("%s: trace = %v, audit = %+v; want only the refusal row", name, r.tr.list(), r.aw.events)
		}
	}
	r := newRig()
	if err := r.writer(t, v).Write(context.Background(), good, []byte("v")); err != nil || r.tr.count("set") != 1 {
		t.Fatalf("matching record: Write = %v, sets = %d", err, r.tr.count("set"))
	}
}

// TestClipboardWriteBlocksUntilClear: Write returns only after the TTL
// clear, and the TTL is what was armed.
func TestClipboardWriteBlocksUntilClear(t *testing.T) {
	r := newRig()
	r.sched = &fakeScheduler{armed: make(chan time.Duration, 1)}
	done := make(chan error, 1)
	w := r.writer(t, nil)
	go func() { done <- w.Write(context.Background(), []byte("tok"), []byte("v")) }()
	if d := <-r.sched.armed; d != 30*time.Second {
		t.Fatalf("armed %s, want the 30s TTL", d)
	}
	if r.tr.count("clear") != 0 {
		t.Fatal("cleared before the TTL fired")
	}
	r.sched.fire(0)
	if err := <-done; err != nil || r.tr.count("confirm:clipboard.clear") != 1 || r.store.len() != 0 {
		t.Fatalf("Write = %v, trace = %v", err, r.tr.list())
	}
}

// TestClipboardCancelClearsAtOnce: cancelling the context (SIGINT/SIGTERM)
// clears without waiting for the TTL, exactly once.
func TestClipboardCancelClearsAtOnce(t *testing.T) {
	r := newRig()
	r.sched = &fakeScheduler{armed: make(chan time.Duration, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	w := r.writer(t, nil)
	go func() { done <- w.Write(ctx, []byte("tok"), []byte("v")) }()
	<-r.sched.armed
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Write = %v", err)
	}
	r.sched.fire(0) // the stopped timer firing late must not clear twice
	if r.tr.count("clear") != 1 || r.sched.stopped != 1 || r.store.len() != 0 {
		t.Fatalf("clears = %d, stopped = %d, rows = %d", r.tr.count("clear"), r.sched.stopped, r.store.len())
	}
}

func rearmRig(rows ...PendingClear) *rig {
	r := newRig()
	r.sched = &fakeScheduler{}
	for _, row := range rows {
		r.store.rows[row.RefID] = row
	}
	return r
}

func (r *rig) rearm(t *testing.T) error {
	t.Helper()
	return rearmWithOps(context.Background(), runtime.NewFixedClock(clipNow), r.fx, r.store, r.sched,
		func() (clipboardOps, error) { return r.ops, nil })
}

func row(ref string, due time.Time) PendingClear {
	return PendingClear{RefID: ref, Platform: "fake", WrittenAt: due.Add(-30 * time.Second), ClearDueAt: due}
}

// TestRearmHostClearsOverdueRow: a row past due clears now, confirmed, row
// removed; nothing is scheduled.
func TestRearmHostClearsOverdueRow(t *testing.T) {
	r := rearmRig(row("OVERDUE1", clipNow.Add(-time.Second)))
	if err := r.rearm(t); err != nil {
		t.Fatalf("rearm: %v", err)
	}
	if r.tr.count("confirm:clipboard.clear") != 1 || r.store.len() != 0 || len(r.sched.fns) != 0 {
		t.Fatalf("trace = %v, rows = %d, armed = %d", r.tr.list(), r.store.len(), len(r.sched.fns))
	}
}

// TestRearmHostSchedulesRemaining: a not-yet-due row is armed for exactly
// the remainder and clears when it fires.
func TestRearmHostSchedulesRemaining(t *testing.T) {
	r := rearmRig(row("NOTYET1", clipNow.Add(12*time.Second)))
	if err := r.rearm(t); err != nil {
		t.Fatalf("rearm: %v", err)
	}
	if len(r.sched.delays) != 1 || r.sched.delays[0] != 12*time.Second || r.tr.count("clear") != 0 {
		t.Fatalf("delays = %v, trace = %v; want one 12s arm and no clear yet", r.sched.delays, r.tr.list())
	}
	r.sched.fire(0)
	if r.tr.count("confirm:clipboard.clear") != 1 || r.store.len() != 0 {
		t.Fatalf("after firing: trace = %v, rows = %d", r.tr.list(), r.store.len())
	}
}

// TestRearmRetriesFailedClear: a failing due clear keeps its row, does not
// refuse the start, and is re-armed for a retry.
func TestRearmRetriesFailedClear(t *testing.T) {
	r := rearmRig(row("FAILS1", clipNow))
	r.ops.clearErr = errors.New("no display")
	if err := r.rearm(t); err != nil {
		t.Fatalf("rearm = %v; a failed clear must not refuse the start", err)
	}
	if r.store.len() != 1 || len(r.sched.delays) != 1 || r.sched.delays[0] != clearRetryInterval {
		t.Fatalf("rows = %d, delays = %v; want the row kept and one retry armed", r.store.len(), r.sched.delays)
	}
	r.ops.clearErr = nil
	r.sched.fire(0)
	if r.store.len() != 0 {
		t.Fatalf("the retry did not clear: %v", r.tr.list())
	}
}

// TestRearmSettlesOnlyClipboardIntents: open clipboard intents become
// unknown-outcome; another site's open intent is left alone.
func TestRearmSettlesOnlyClipboardIntents(t *testing.T) {
	r := rearmRig()
	ctx := context.Background()
	for _, key := range []string{"clipboard.write:REF1", "approval.grant:X"} {
		if _, err := r.fx.BeginEffect(ctx, effectRequest(clipboardWriteAction, key, PendingClear{})); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.rearm(t); err != nil {
		t.Fatalf("rearm: %v", err)
	}
	if r.fx.phaseOf("clipboard.write:REF1") != "unknown-outcome" || r.fx.phaseOf("approval.grant:X") != "intent" {
		t.Fatalf("phases = %v", r.fx.phase)
	}
}

// TestRearmRetriesAreBounded: a clear that keeps failing is retried exactly
// clearRetries times after the first attempt, then the row waits.
func TestRearmRetriesAreBounded(t *testing.T) {
	r := rearmRig(row("FAILS2", clipNow))
	r.ops.clearErr = errors.New("no display")
	if err := r.rearm(t); err != nil {
		t.Fatalf("rearm = %v", err)
	}
	for i := 0; i < clearRetries; i++ {
		r.sched.fire(i)
	}
	r.sched.fire(clearRetries) // the last retry fails and arms nothing
	if len(r.sched.fns) != clearRetries+1 || r.tr.count("clear") != clearRetries+2 || r.store.len() != 1 {
		t.Fatalf("arms = %d, clears = %d, rows = %d", len(r.sched.fns), r.tr.count("clear"), r.store.len())
	}
}

// TestRearmScheduledClearEndsWithDaemon: once the daemon's lifetime ends its
// scheduled clears are stopped and a late fire touches nothing; the row
// waits for the next start.
func TestRearmScheduledClearEndsWithDaemon(t *testing.T) {
	r := rearmRig(row("LATER1", clipNow.Add(time.Minute)))
	r.sched.onStop = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	if err := rearmWithOps(ctx, runtime.NewFixedClock(clipNow), r.fx, r.store, r.sched,
		func() (clipboardOps, error) { return r.ops, nil }); err != nil {
		t.Fatalf("rearm: %v", err)
	}
	cancel()
	<-r.sched.onStop
	r.sched.fire(0)
	if r.tr.count("clear") != 0 || r.tr.count("begin:clipboard.clear") != 0 || r.store.len() != 1 {
		t.Fatalf("trace = %v, rows = %d; want nothing run after the daemon stopped", r.tr.list(), r.store.len())
	}
}
