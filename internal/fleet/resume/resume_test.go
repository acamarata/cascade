// Purpose: construction validation, cold start, and the Windows tier-2
//   refusal (tasks 1, 6, 7's cold-start leg).
// Constraints: Art.7.1 (t.TempDir only); no bare time.Now (testkit.FrozenClock).
// SPORT: internal.fleet.resume.ResumeManager/ADDED (tests) (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

var testInstant = time.Unix(1_700_000_000, 0).UTC()

// newRealStore opens a real on-disk SQLite journal store (the same driver
// production uses) under t.TempDir. Returned alongside the raw
// provider.Store and its path so a test can also seed raw bytes or reopen
// a second store instance against the identical file (simulating a
// restart).
func newRealStore(t *testing.T) (journal.Store, provider.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.db")
	driver, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	return journal.New(driver, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace), driver, path
}

// fakeFanOut is the test double for FanOutFunc: it records every call and
// returns preset legs/err, without touching any real provider.
func fakeFanOut(calls *[]fakeFanOutCall, legs []provider.ModelResponse, err error) FanOutFunc {
	return func(_ context.Context, req provider.ModelRequest, n int, completed map[int]conductor.JobID, _ conductor.WithPermitFn, _ conductor.JournalAppender) ([]provider.ModelResponse, error) {
		*calls = append(*calls, fakeFanOutCall{req: req, n: n, completed: completed})
		return legs, err
	}
}

type fakeFanOutCall struct {
	req       provider.ModelRequest
	n         int
	completed map[int]conductor.JobID
}

// poisonStore fails t.Fatal if ANY method is called — used to prove the
// Windows refusal never touches the journal at all.
type poisonStore struct{ t *testing.T }

func (p poisonStore) Append(context.Context, string, journal.Kind, string, json.RawMessage) (journal.Entry, error) {
	p.t.Fatal("poisonStore.Append called: Windows refusal must not touch the journal")
	return journal.Entry{}, nil
}
func (p poisonStore) Checkpoint(context.Context, journal.Cursor) error {
	p.t.Fatal("poisonStore.Checkpoint called")
	return nil
}
func (p poisonStore) Replay(context.Context, string, journal.Cursor, []journal.Kind) ([]journal.Entry, error) {
	p.t.Fatal("poisonStore.Replay called")
	return nil, nil
}
func (p poisonStore) ListEntities(context.Context) ([]string, error) {
	p.t.Fatal("poisonStore.ListEntities called")
	return nil, nil
}
func (p poisonStore) Recover(context.Context, string) (journal.TruncationReport, error) {
	p.t.Fatal("poisonStore.Recover called")
	return journal.TruncationReport{}, nil
}
func (p poisonStore) Close() error { return nil }

func TestNew_RequiresJournalAndFanOut(t *testing.T) {
	store, _, _ := newRealStore(t)
	var calls []fakeFanOutCall
	fo := fakeFanOut(&calls, nil, nil)

	if _, err := New(nil, fo, nil, nil, nil, nil, "darwin"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("New(nil journal, ...) = %v, want KindInvalidInput", err)
	}
	if _, err := New(store, nil, nil, nil, nil, nil, "darwin"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("New(journal, nil fanOut, ...) = %v, want KindInvalidInput", err)
	}
	if _, err := New(store, fo, nil, nil, nil, nil, "darwin"); err != nil {
		t.Fatalf("New with only required deps: %v, want nil", err)
	}
}

func TestResumeColdStart(t *testing.T) {
	store, _, _ := newRealStore(t)
	var calls []fakeFanOutCall
	mgr, err := New(store, fakeFanOut(&calls, nil, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run on an empty journal: %v, want nil (cold start is not a failure)", err)
	}
	if !report.ColdStart {
		t.Fatalf("Report.ColdStart = false, want true on an empty journal")
	}
	if len(calls) != 0 {
		t.Fatalf("fanOut called %d times on a cold start, want 0", len(calls))
	}
}

func TestResumeWindowsTier2Refusal(t *testing.T) {
	var calls []fakeFanOutCall
	mgr, err := New(poisonStore{t: t}, fakeFanOut(&calls, nil, nil), nil, nil, nil, nil, "windows")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = mgr.Run(context.Background())
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("Run on windows = %v, want KindUnsupported", err)
	}
	if len(calls) != 0 {
		t.Fatalf("fanOut called on the windows refusal path, want 0 calls")
	}
}

func TestRefuseOnGOOS(t *testing.T) {
	cases := map[string]bool{"windows": true, "darwin": false, "linux": false, "": false}
	for goos, wantErr := range cases {
		err := RefuseOnGOOS(goos)
		if (err != nil) != wantErr {
			t.Errorf("RefuseOnGOOS(%q) = %v, want error=%v", goos, err, wantErr)
		}
		if err != nil && !cascade.HasKind(err, cascade.KindUnsupported) {
			t.Errorf("RefuseOnGOOS(%q) = %v, want KindUnsupported", goos, err)
		}
	}
}
