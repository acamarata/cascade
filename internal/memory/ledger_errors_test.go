package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObserveRefusesUnusableEvidence(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		obs  Observation
		want error
	}{
		{"empty session", Observation{SessionID: "", Draft: validEntry()}, ErrInvalidSessionID},
		{"control byte in session", Observation{SessionID: "a\nb", Draft: validEntry()}, ErrInvalidSessionID},
		{"over-long session", Observation{
			SessionID: strings.Repeat("x", maxSessionIDLen+1), Draft: validEntry(),
		}, ErrInvalidSessionID},
		{"unusable name", Observation{SessionID: "s-1", Draft: func() MemoryEntry {
			e := validEntry()
			e.Name = "../escape"
			return e
		}()}, ErrInvalidName},
		{"unknown kind", Observation{SessionID: "s-1", Draft: func() MemoryEntry {
			e := validEntry()
			e.Kind = "invented"
			return e
		}()}, ErrInvalidKind},
		{"draft with no scope", Observation{SessionID: "s-1", Draft: func() MemoryEntry {
			e := validEntry()
			e.ScopeRef = ""
			return e
		}()}, ErrInvalidScopeRef},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLedger(t)
			_, err := f.ledger.Observe(ctx, tc.obs)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Observe: %v, want %v", err, tc.want)
			}
			if names, lerr := f.ledger.List(ctx, KindProject); lerr != nil || len(names) != 0 {
				t.Errorf("refused evidence still created a candidate: %v (%v)", names, lerr)
			}
		})
	}
}

// TestDamagedCandidateRefusesRatherThanRestarting is the fail-closed rule:
// evidence that cannot be read is never treated as no evidence, because
// that would silently reset a count or re-promote a promoted candidate.
func TestDamagedCandidateRefusesRatherThanRestarting(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		content string
		want    error
	}{
		{"not json", "{not json", ErrMalformedCandidate},
		{"no version", `{"name":"a-record","kind":"project","status":"pending"}`, ErrMalformedCandidate},
		{"unknown status", `{"format":1,"name":"a-record","kind":"project","status":"levitating"}`,
			ErrMalformedCandidate},
		{"count below sessions", `{"format":1,"name":"a-record","kind":"project",` +
			`"status":"pending","ref_count":1,"session_ids":["s-1","s-2"]}`, ErrMalformedCandidate},
		{"newer format", `{"format":2,"name":"a-record","kind":"project","status":"pending"}`,
			ErrUnsupportedCandidateFormat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLedger(t)
			writeCandidateFile(t, f.base, "a-record", tc.content)

			if _, err := f.ledger.Get(ctx, KindProject, "a-record"); !errors.Is(err, tc.want) {
				t.Errorf("Get: %v, want %v", err, tc.want)
			}
			_, promoted, err := NewPromotionLadder(f.ledger).Observe(ctx, observation("s-1"))
			if !errors.Is(err, tc.want) {
				t.Errorf("Observe: %v, want %v", err, tc.want)
			}
			if promoted {
				t.Error("unreadable evidence promoted a candidate")
			}
			if _, rerr := f.store.Read(ctx, KindProject, "a-record"); !errors.Is(rerr, ErrNoSuchEntry) {
				t.Errorf("a durable record was written from unreadable evidence: %v", rerr)
			}
		})
	}
}

// writeCandidateFile plants a candidate file directly, which is how a test
// reaches states only a damaged disk or a newer build could produce.
func writeCandidateFile(t *testing.T, base, name, content string) {
	t.Helper()
	dir := filepath.Join(base, "candidates", "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating candidate dir: %v", err)
	}
	path := filepath.Join(dir, name+".candidate.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing candidate file: %v", err)
	}
}

func TestListReturnsSortedNamesAndSurvivesDamage(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	for _, n := range []string{"beta", "alpha"} {
		e := validEntry()
		e.Name = n
		if _, err := f.ledger.Observe(ctx, Observation{SessionID: "s-1", Draft: e}); err != nil {
			t.Fatalf("Observe(%s): %v", n, err)
		}
	}
	writeCandidateFile(t, f.base, "damaged", "{not json")
	if err := os.WriteFile(
		filepath.Join(f.base, "candidates", "project", "notes.txt"), []byte("x"), 0o600,
	); err != nil {
		t.Fatalf("writing unrelated file: %v", err)
	}

	got, err := f.ledger.List(ctx, KindProject)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha", "beta", "damaged"}
	if len(got) != len(want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List = %v, want %v", got, want)
		}
	}
	if _, err := f.ledger.List(ctx, "invented"); !errors.Is(err, ErrInvalidKind) {
		t.Errorf("List with an unknown kind: %v, want ErrInvalidKind", err)
	}
}

func TestFileSystemFailuresAreTypedAndPromoteNothing(t *testing.T) {
	ctx := context.Background()
	sys := newFailingFS()
	store := NewFileStore(t.TempDir(), newTestClock())
	sink := &recordingSink{}
	l := newCandidateLedgerWithFS(t.TempDir(), store, newTestClock(), sink, sys)

	sys.failWrite = true
	if _, err := l.Observe(ctx, observation("s-1")); !errors.Is(err, ErrStoreIO) {
		t.Errorf("Observe with a failing write: %v, want ErrStoreIO", err)
	}
	sys.failWrite = false
	observeAll(t, l, "s-1", "s-2", "s-3")

	sys.failRead = true
	if _, err := l.Get(ctx, KindProject, "a-record"); !errors.Is(err, ErrStoreIO) {
		t.Errorf("Get with a failing read: %v, want ErrStoreIO", err)
	}
	if _, err := l.Promote(ctx, KindProject, "a-record"); !errors.Is(err, ErrStoreIO) {
		t.Errorf("Promote with a failing read: %v, want ErrStoreIO", err)
	}
	sys.failRead = false
	sys.failListDir = true
	if _, err := l.List(ctx, KindProject); !errors.Is(err, ErrStoreIO) {
		t.Errorf("List with a failing directory read: %v, want ErrStoreIO", err)
	}
	if len(sink.promotions) != 0 {
		t.Errorf("a failing file system still promoted: %+v", sink.promotions)
	}
}

// TestPromoteLeavesStatePendingWhenTheDurableWriteFails proves the write
// order: no candidate claims a record the store refused to write.
func TestPromoteLeavesStatePendingWhenTheDurableWriteFails(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	e := validEntry()
	e.ExpiresAt = ptrTime(fixedNow.Add(time.Hour))
	for _, s := range []string{"s-1", "s-2", "s-3"} {
		if _, err := f.ledger.Observe(ctx, Observation{SessionID: s, Draft: e}); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	f.clock.advance(2 * time.Hour) // the draft's TTL passes before promotion

	if _, err := f.ledger.Promote(ctx, KindProject, "a-record"); !errors.Is(err, ErrAlreadyExpired) {
		t.Fatalf("Promote with an expired draft: %v, want ErrAlreadyExpired", err)
	}
	got, err := f.ledger.Get(ctx, KindProject, "a-record")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != CandidatePending {
		t.Errorf("status after a failed durable write = %q, want %q", got.Status, CandidatePending)
	}
	if len(f.sink.promotions) != 0 {
		t.Errorf("a failed durable write emitted a promotion: %+v", f.sink.promotions)
	}
}

func TestCandidateStatusParsing(t *testing.T) {
	for _, s := range []string{"pending", "promoted", "reverted"} {
		got, err := ParseCandidateStatus(s)
		if err != nil || got.String() != s {
			t.Errorf("ParseCandidateStatus(%q) = %q, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "PENDING", "promoted ", "levitating"} {
		if _, err := ParseCandidateStatus(s); !errors.Is(err, ErrInvalidCandidateStatus) {
			t.Errorf("ParseCandidateStatus(%q) = %v, want ErrInvalidCandidateStatus", s, err)
		}
	}
}

func TestCanceledContextIsRefusedByEveryLedgerMethod(t *testing.T) {
	f := newLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.ledger.Observe(ctx, observation("s-1")); err == nil {
		t.Error("Observe accepted a canceled context")
	}
	if _, err := f.ledger.Get(ctx, KindProject, "a-record"); err == nil {
		t.Error("Get accepted a canceled context")
	}
	if _, err := f.ledger.Promote(ctx, KindProject, "a-record"); err == nil {
		t.Error("Promote accepted a canceled context")
	}
	if _, err := f.ledger.Revert(ctx, KindProject, "a-record", ""); err == nil {
		t.Error("Revert accepted a canceled context")
	}
	if _, err := f.ledger.List(ctx, KindProject); err == nil {
		t.Error("List accepted a canceled context")
	}
}

// TestSinkFailureIsReportedNotSwallowed: the transition is already
// persisted when the sink is called, so the caller is told the event did
// not land rather than being left to assume it did.
func TestSinkFailureIsReportedNotSwallowed(t *testing.T) {
	ctx := context.Background()
	f := newLedger(t)
	f.sink.failWith = errors.New("bus unavailable")
	observeAll(t, f.ledger, "s-1", "s-2", "s-3")

	if _, err := f.ledger.Promote(ctx, KindProject, "a-record"); err == nil {
		t.Error("Promote hid a sink failure")
	}
	if _, err := f.ledger.Revert(ctx, KindProject, "a-record", "x"); err == nil {
		t.Error("Revert hid a sink failure")
	}
	got, err := f.ledger.Get(ctx, KindProject, "a-record")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != CandidateReverted {
		t.Errorf("status = %q, want %q: the transition is persisted before the event",
			got.Status, CandidateReverted)
	}
}
