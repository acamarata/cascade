// Purpose: the clipboard writer's failure paths over the shared fakes: a
//
//	refused intent or row, a confirm that fails after the value is on the
//	clipboard, an unaudited clear, a row that cannot be removed, backend
//	error kinds, refusals, constructor checks, the system scheduler and the
//	re-arm refusals. No test touches the real clipboard or HOME.
//
// SPORT: internal/secrets ClipboardWriter/TEST (P1-SEC-09).
package secrets

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestClipboardAuditFailureRefuses: a refused intent stops the write with
// the clipboard untouched and no row.
func TestClipboardAuditFailureRefuses(t *testing.T) {
	r := newRig()
	r.fx.failBegin, r.fx.beginErr = "clipboard.write", errors.New("audit store down")
	err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "audit store down") {
		t.Fatalf("Write = %v, want the KindUnavailable audit refusal", err)
	}
	if r.tr.count("set") != 0 || r.tr.count("put") != 0 || r.store.len() != 0 {
		t.Fatalf("trace = %v; nothing may be persisted or written", r.tr.list())
	}
}

// TestClipboardPutFailureFailsIntent: no row means no write, and the intent
// is closed failed.
func TestClipboardPutFailureFailsIntent(t *testing.T) {
	r := newRig()
	r.store.putErr = errors.New("disk full")
	if err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v")); err == nil {
		t.Fatal("Write succeeded with no durable row")
	}
	if r.tr.count("set") != 0 || r.tr.count("fail:clipboard.write") != 1 {
		t.Fatalf("trace = %v; want the intent failed and no set", r.tr.list())
	}
}

// TestClipboardConfirmFailureClearsAtOnce: when the write's confirm cannot be
// recorded after the value reached the clipboard, the clear runs at once
// (not at the TTL, nothing is armed), and because that clear cannot be
// confirmed either the row is kept for the daemon.
func TestClipboardConfirmFailureClearsAtOnce(t *testing.T) {
	r := newRig()
	r.sched = &fakeScheduler{}
	r.fx.confirmErr = errors.New("audit store down")
	err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "immediate clear did not complete") ||
		strings.Contains(err.Error(), "the value was cleared") {
		t.Fatalf("Write = %v, want the KindUnavailable confirm failure that claims no completed clear", err)
	}
	want := []string{"probe", "begin:clipboard.write", "put", "set", "confirm:clipboard.write",
		"begin:clipboard.clear", "clear", "confirm:clipboard.clear"}
	if got := r.tr.list(); !slices.Equal(got, want) {
		t.Fatalf("order = %v\nwant    %v", got, want)
	}
	if len(r.sched.fns) != 0 || r.store.len() != 1 {
		t.Fatalf("armed = %d, rows = %d; want an immediate clear and the row kept", len(r.sched.fns), r.store.len())
	}
	// Only the write's confirm fails: the immediate clear completes, is
	// confirmed, and its row is removed.
	r = newRig()
	r.sched = &fakeScheduler{}
	r.fx.failConfirm = "clipboard.write"
	err = r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "cleared at once") ||
		r.tr.count("confirm:clipboard.clear") != 1 || r.store.len() != 0 || len(r.sched.fns) != 0 {
		t.Fatalf("Write = %v, trace = %v, rows = %d", err, r.tr.list(), r.store.len())
	}
}

// TestClipboardClearRunsWhenItsIntentFails: the secret leaving the clipboard
// outranks the record, so an unrecordable clear still clears, and the row
// stays so the re-arm repeats it under a record.
func TestClipboardClearRunsWhenItsIntentFails(t *testing.T) {
	r := newRig()
	r.fx.failBegin, r.fx.beginErr = "clipboard.clear", errors.New("audit store down")
	err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "could not be audited") {
		t.Fatalf("Write = %v, want the unaudited-clear failure", err)
	}
	if r.tr.count("clear") != 1 || r.store.len() != 1 || r.tr.count("delete") != 0 {
		t.Fatalf("trace = %v, rows = %d; want the clear run and the row kept", r.tr.list(), r.store.len())
	}
	// Both fail: the clear error leads and names the audit failure too.
	r = newRig()
	r.fx.failBegin, r.fx.beginErr = "clipboard.clear", errors.New("audit store down")
	r.ops.clearErr = errors.New("pbcopy exit 1")
	err = r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !strings.Contains(err.Error(), "pbcopy exit 1") || !strings.Contains(err.Error(), "audit store down") || r.store.len() != 1 {
		t.Fatalf("Write = %v, rows = %d", err, r.store.len())
	}
	if r.tr.count("unknown:clipboard.clear") != 0 {
		t.Fatal("an intent that was never begun was marked unknown-outcome")
	}
}

// TestClipboardRowDeleteFailureReported: a confirmed clear whose row cannot
// be removed reports it; the daemon clears again later.
func TestClipboardRowDeleteFailureReported(t *testing.T) {
	r := newRig()
	r.store.delErr = errors.New("read-only file system")
	err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "pending row could not be removed") {
		t.Fatalf("Write = %v", err)
	}
	if r.tr.count("confirm:clipboard.clear") != 1 || r.store.len() != 1 {
		t.Fatalf("trace = %v, rows = %d", r.tr.list(), r.store.len())
	}
}

// TestClipboardSetFailureKeepsBackendKind: a backend error that already
// carries a taxonomy kind keeps it; a bare OS error becomes
// ErrClipboardUnavailable.
func TestClipboardSetFailureKeepsBackendKind(t *testing.T) {
	r := newRig()
	r.ops.setErr = cascade.New(cascade.KindUnsupported, "no display")
	if err := r.writer(t, nil).Write(context.Background(), []byte("tok"), []byte("v")); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Write = %v, want the backend's KindUnsupported", err)
	}
	if err := setValueError(errors.New("exit 1")); !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "exit 1") {
		t.Fatalf("setValueError = %v", err)
	}
}

// TestClipboardWrongVerbRefusedAndAuditFailureNamed: a verified record for
// another verb refuses; a refusal whose audit row fails names both.
func TestClipboardWrongVerbRefusedAndAuditFailureNamed(t *testing.T) {
	r := newRig()
	r.aw.err = errors.New("audit store down")
	err := r.writer(t, fakeVerifier{verb: "vault.get"}).Write(context.Background(), []byte("tok"), []byte("v"))
	if !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), ClipboardApprovalVerb) ||
		!strings.Contains(err.Error(), "audit store down") {
		t.Fatalf("Write = %v, want the refusal naming the verb and the audit failure", err)
	}
	if len(r.tr.list()) != 0 {
		t.Fatalf("trace = %v; the backend was reached", r.tr.list())
	}
}

// TestClipboardWriterConstructorRefusals: a missing dependency, an effect
// writer that cannot mark unknown-outcome, and an out-of-range TTL refuse.
func TestClipboardWriterConstructorRefusals(t *testing.T) {
	r := newRig()
	clock := runtime.NewFixedClock(clipNow)
	v := fakeVerifier{verb: ClipboardApprovalVerb}
	if _, err := newClipboardWriterWithOps(nil, r.aw, r.fx, v, r.store, r.sched, r.ops, 30*time.Second); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil clock = %v", err)
	}
	if _, err := newClipboardWriterWithOps(clock, r.aw, plainEffects{r.fx}, v, r.store, r.sched, r.ops, 30*time.Second); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("plain effect writer = %v", err)
	}
	if _, err := newClipboardWriterWithOps(clock, r.aw, r.fx, v, r.store, r.sched, r.ops, time.Second); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("1s TTL = %v", err)
	}
}

// plainEffects hides MarkUnknownOutcome.
type plainEffects struct{ audit.EffectWriter }

// TestSystemClipboardSchedulerFiresAndStops: the production scheduler runs
// fn after the delay, and a stopped timer never runs.
func TestSystemClipboardSchedulerFiresAndStops(t *testing.T) {
	s := NewSystemClipboardScheduler()
	fired := make(chan struct{})
	s.AfterFunc(time.Millisecond, func() { close(fired) })
	<-fired
	stop := s.AfterFunc(time.Hour, func() { t.Error("a stopped timer fired") })
	stop()
}

// TestRearmRefusals: missing dependencies, an unlistable store, a backend
// that cannot be built, and an unreadable or unsettleable effect log refuse.
func TestRearmRefusals(t *testing.T) {
	ops := func(r *rig) func() (clipboardOps, error) { return func() (clipboardOps, error) { return r.ops, nil } }
	clock := runtime.NewFixedClock(clipNow)
	r := rearmRig()
	//nolint:staticcheck // a nil context is the refusal under test
	if err := rearmWithOps(nil, clock, r.fx, r.store, r.sched, ops(r)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil ctx = %v", err)
	}
	if err := rearmWithOps(context.Background(), clock, plainEffects{r.fx}, r.store, r.sched, ops(r)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("plain effects = %v", err)
	}
	r.store.listErr = errors.New("disk gone")
	if err := r.rearm(t); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("list failure = %v", err)
	}
	r = rearmRig(row("ROW1", clipNow))
	if err := rearmWithOps(context.Background(), clock, r.fx, r.store, r.sched, func() (clipboardOps, error) {
		return nil, ErrTier2Unsupported
	}); !cascade.HasKind(err, cascade.KindUnsupported) || r.store.len() != 1 {
		t.Fatalf("no backend = %v, rows = %d", err, r.store.len())
	}
	r = rearmRig()
	r.fx.pendingErr = errors.New("log unreadable")
	if err := r.rearm(t); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("pending failure = %v", err)
	}
	r = rearmRig()
	if _, err := r.fx.BeginEffect(context.Background(), effectRequest(clipboardWriteAction, "clipboard.write:REF2", PendingClear{})); err != nil {
		t.Fatal(err)
	}
	r.fx.unknownErr = errors.New("append failed")
	if err := r.rearm(t); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("settle failure = %v", err)
	}
}
