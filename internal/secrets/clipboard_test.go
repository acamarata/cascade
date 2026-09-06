// Purpose: shared-logic tests for the clipboard fallback (approval gating,
// zero-payload audit, red-team byte-absence, durable persistence, restart
// re-arm) against a fake clipboardOps. Real-binary and platform
// error-path tests live in the //go:build-tagged sibling files.
// SPORT: internal/secrets clipboard_test.go/ADDED (P1-E08-W2-S16-T4).
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
)

// fakeOps stands in for a real pbcopy/xclip subprocess.
type fakeOps struct {
	name       string
	setErr     error
	clearErr   error
	setCalls   [][]byte
	clearCalls int
}

func (f *fakeOps) platform() string { return f.name }
func (f *fakeOps) setValue(_ context.Context, payload []byte) error {
	f.setCalls = append(f.setCalls, append([]byte(nil), payload...))
	return f.setErr
}
func (f *fakeOps) clearValue(context.Context) error {
	f.clearCalls++
	return f.clearErr
}

// fakeVerifier lets a test choose whether Verify succeeds.
type fakeVerifier struct {
	rec *policy.ApprovalRecord
	err error
}

func (v *fakeVerifier) Verify([]byte) (*policy.ApprovalRecord, error) {
	if v.err != nil {
		return nil, v.err
	}
	return v.rec, nil
}

// fakeAudit captures every appended event, byte for byte.
type fakeAudit struct{ events []audit.Event }

func (a *fakeAudit) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	a.events = append(a.events, e)
	return audit.Record{Event: e}, nil
}

// fakeStore is an in-memory PendingClearStore.
type fakeStore struct {
	rows   map[string]PendingClear
	putErr error
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]PendingClear{}} }

func (s *fakeStore) Put(_ context.Context, pc PendingClear) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.rows[pc.RefID] = pc
	return nil
}
func (s *fakeStore) List(context.Context) ([]PendingClear, error) {
	out := make([]PendingClear, 0, len(s.rows))
	for _, v := range s.rows {
		out = append(out, v)
	}
	return out, nil
}
func (s *fakeStore) Delete(_ context.Context, refID string) error {
	delete(s.rows, refID)
	return nil
}

// fakeScheduler never fires on its own; a test calls the captured fn
// directly — deterministic, no real sleep.
type fakeScheduler struct{ fns []func() }

func (s *fakeScheduler) AfterFunc(_ time.Duration, fn func()) (stop func()) {
	s.fns = append(s.fns, fn)
	return func() {}
}
func (s *fakeScheduler) fireAll() {
	for _, fn := range s.fns {
		fn()
	}
	s.fns = nil
}

func okVerifier() *fakeVerifier { return &fakeVerifier{rec: &policy.ApprovalRecord{}} }

func newTestWriter(t *testing.T, clock runtime.Clock, ops *fakeOps, aw *fakeAudit, store *fakeStore, sched *fakeScheduler, v *fakeVerifier) ClipboardWriter {
	t.Helper()
	w, err := newClipboardWriterWithOps(clock, aw, v, store, sched, ops)
	if err != nil {
		t.Fatalf("newClipboardWriterWithOps: %v", err)
	}
	return w
}

// TestClipboard_Darwin: write, then the injected clock's countdown fires
// and the clipboard is cleared, never left holding the payload.
func TestClipboard_Darwin(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(1000, 0))
	ops := &fakeOps{name: "darwin"}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	if err := w.Write(context.Background(), []byte("signed"), []byte("s3cr3t-value")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if ops.clearCalls != 0 || len(store.rows) != 1 {
		t.Fatalf("clear ran early (%d) or clear not persisted (%d rows)", ops.clearCalls, len(store.rows))
	}
	clock.Advance(clipboardClearTTL)
	sched.fireAll()
	if ops.clearCalls != 1 || len(store.rows) != 0 {
		t.Fatalf("clear did not fire once and drop its row: %d calls, %d rows", ops.clearCalls, len(store.rows))
	}
}

// TestClipboard_Linux mirrors the darwin success path over the shared
// logic; the ops implementation differs only in clipboard_linux_test.go.
func TestClipboard_Linux(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	ops := &fakeOps{name: "linux"}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	if err := w.Write(context.Background(), []byte("signed"), []byte("linux-secret")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	clock.Advance(clipboardClearTTL)
	sched.fireAll()
	if ops.clearCalls != 1 {
		t.Fatalf("expected clear to fire once, got %d", ops.clearCalls)
	}
}

// TestClipboardDoesNotClobberIfCleared: the AUDIT CONTRACT records the
// cleared_at update regardless of the clear subprocess's own error.
func TestClipboardDoesNotClobberIfCleared(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(3000, 0))
	ops := &fakeOps{name: "darwin", clearErr: errors.New("boom")}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	if err := w.Write(context.Background(), []byte("signed"), []byte("v")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	sched.fireAll()
	if len(aw.events) != 2 || len(store.rows) != 0 {
		t.Fatalf("write+clear events (%d) or row cleanup (%d rows) wrong on a failing clear", len(aw.events), len(store.rows))
	}
}

// TestClipboardRefusesWithoutApprovalToken: R-21.242 gate — refuse before
// any subprocess, persistence or audit.
func TestClipboardRefusesWithoutApprovalToken(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	ops := &fakeOps{name: "darwin"}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	v := &fakeVerifier{err: policy.ErrExpired}
	w := newTestWriter(t, clock, ops, aw, store, sched, v)

	err := w.Write(context.Background(), []byte("expired-or-forged"), []byte("secret"))
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Write = %v, want ErrApprovalRequired", err)
	}
	if len(ops.setCalls) != 0 || len(aw.events) != 0 || len(store.rows) != 0 {
		t.Fatalf("a refused approval must touch nothing: %d subprocess calls, %d events, %d rows",
			len(ops.setCalls), len(aw.events), len(store.rows))
	}
}

// TestClipboard_AuditEvent_NoPayload: the serialised event never carries
// the payload; cleared_at populates only once the clear fires.
func TestClipboard_AuditEvent_NoPayload(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(5000, 0))
	ops := &fakeOps{name: "darwin"}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	payload := "UNIQUE-PAYLOAD-MARKER-0xdeadbeef"
	if err := w.Write(context.Background(), []byte("signed"), []byte(payload)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(aw.events) != 1 || strings.Contains(string(aw.events[0].Explain), payload) {
		t.Fatalf("write event count/leak wrong: %v", aw.events)
	}
	if strings.Contains(string(aw.events[0].Explain), `"cleared_at":"`) {
		t.Fatalf("cleared_at populated before the clear fired: %s", aw.events[0].Explain)
	}

	clock.Advance(clipboardClearTTL)
	sched.fireAll()
	if len(aw.events) != 2 {
		t.Fatalf("expected a second event at clear time, got %d", len(aw.events))
	}
	second := aw.events[1]
	if strings.Contains(string(second.Explain), payload) || !strings.Contains(string(second.Explain), `"cleared_at":"`) {
		t.Fatalf("clear-time event leaked payload or missing cleared_at: %s", second.Explain)
	}
}

// TestClipboard_RedTeam_ArgsClean: payload bytes appear exactly once, at
// the ops.setValue stand-in for the subprocess stdin pipe, and nowhere
// else this package produces (never argv per clipboard_darwin.go /
// clipboard_linux.go, never a log line, never a serialised audit event).
func TestClipboard_RedTeam_ArgsClean(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(6000, 0))
	ops := &fakeOps{name: "darwin"}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	secret := "RED-TEAM-SECRET-bytes-must-never-leak"
	if err := w.Write(context.Background(), []byte("signed"), []byte(secret)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	clock.Advance(clipboardClearTTL)
	sched.fireAll()

	if len(ops.setCalls) != 1 || string(ops.setCalls[0]) != secret {
		t.Fatalf("setValue did not receive the payload exactly once")
	}
	for _, e := range aw.events {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		if strings.Contains(string(b), secret) {
			t.Fatalf("serialised event leaked the secret: %s", b)
		}
	}
}

// TestClipboardPendingClearRearmedOnBoot simulates a crash-restart: a row
// already past ClearDueAt fires immediately; one not yet due reschedules.
func TestClipboardPendingClearRearmedOnBoot(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(9000, 0))
	ops := &fakeOps{name: "darwin"}
	aw, store, sched := &fakeAudit{}, newFakeStore(), &fakeScheduler{}
	w := newTestWriter(t, clock, ops, aw, store, sched, okVerifier())

	store.rows["past"] = PendingClear{
		RefID: "past", Platform: "darwin",
		WrittenAt: clock.Now().Add(-time.Minute), ClearDueAt: clock.Now().Add(-time.Second),
	}
	store.rows["future"] = PendingClear{
		RefID: "future", Platform: "darwin",
		WrittenAt: clock.Now(), ClearDueAt: clock.Now().Add(10 * time.Second),
	}

	if err := RearmPendingClears(context.Background(), w, clock, sched, store); err != nil {
		t.Fatalf("RearmPendingClears: %v", err)
	}
	if ops.clearCalls != 1 {
		t.Fatalf("past-due row must clear immediately: got %d", ops.clearCalls)
	}
	if _, pending := store.rows["past"]; pending {
		t.Fatalf("past-due row must be removed once cleared")
	}
	if _, pending := store.rows["future"]; !pending {
		t.Fatalf("not-yet-due row must remain pending")
	}
	if len(sched.fns) != 1 {
		t.Fatalf("not-yet-due row must be re-scheduled, got %d callbacks", len(sched.fns))
	}
	sched.fireAll()
	if ops.clearCalls != 2 {
		t.Fatalf("re-scheduled row did not clear when its callback ran: %d", ops.clearCalls)
	}
}
