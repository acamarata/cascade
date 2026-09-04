// Purpose: the shared fixture for this package's tests — a queue over a
//
//	REAL candidate ledger and a REAL file store in a temp directory, on a
//	frozen clock, plus the tree hash that proves an operation left the
//	store byte-identical. Split from queue_test.go under Art.10.3's
//	300-line file cap.
//
// SPORT: internal/memory/review (ADD, P1-E07-W2-S14-T3).
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/testkit"
)

// fixedNow is the instant every test in this package starts from. A frozen
// clock is not a convenience here: a snooze boundary and a digest window
// are both claims about time, and neither can be asserted against a clock
// that moves on its own.
var fixedNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

// fixture is a queue over a REAL ledger and a REAL file store in a temp
// directory. Nothing here is a double: an approve in these tests writes a
// real record file, and a defer writes a real candidate file.
type fixture struct {
	queue  *Queue
	ledger *memory.FileCandidateLedger
	store  *memory.FileStore
	clock  *testkit.FrozenClock
	sink   *recordingSink
	base   string
}

// recordingSink captures the action events the queue emits.
type recordingSink struct{ actions []ActionEvent }

func (s *recordingSink) ReviewActed(_ context.Context, ev ActionEvent) error {
	s.actions = append(s.actions, ev)
	return nil
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	clk := testkit.NewFrozenClock(fixedNow)
	store := memory.NewFileStore(base, clk)
	ledger := memory.NewFileCandidateLedger(base, store, clk, nil)
	sink := &recordingSink{}
	return fixture{
		queue: NewQueue(ledger, clk, sink), ledger: ledger, store: store,
		clock: clk, sink: sink, base: base,
	}
}

// draft returns a valid record for a candidate of the given kind and name.
func draft(kind memory.MemoryKind, name string) memory.MemoryEntry {
	return memory.MemoryEntry{
		Name: name, Kind: kind,
		Description: "a description for " + name,
		Body:        "a body for " + name + "\n",
		ScopeRef:    "global", Confidence: 0.5,
		Provenance: memory.Provenance{Origin: memory.OriginSession, SessionID: "s-1"},
	}
}

// observe records one observation per session, failing on the first
// refusal, and returns the candidate's state afterwards.
func (f fixture) observe(
	t *testing.T, kind memory.MemoryKind, name string, sessions ...string,
) memory.CandidateEntry {
	t.Helper()
	var got memory.CandidateEntry
	for _, s := range sessions {
		var err error
		got, err = f.ledger.Observe(context.Background(),
			memory.Observation{SessionID: s, Draft: draft(kind, name)})
		if err != nil {
			t.Fatalf("observe %s/%s from %s: %v", kind, name, s, err)
		}
	}
	return got
}

// promote drives a candidate all the way to promoted through the real
// ledger, so the promoted section under test holds a real promotion.
func (f fixture) promote(t *testing.T, kind memory.MemoryKind, name string) {
	t.Helper()
	f.observe(t, kind, name, "s-1", "s-2", "s-3")
	if _, err := f.ledger.Promote(context.Background(), kind, name); err != nil {
		t.Fatalf("promote %s/%s: %v", kind, name, err)
	}
}

// list runs the read path, failing the test on refusal.
func (f fixture) list(t *testing.T, p ListParams) ListResult {
	t.Helper()
	got, err := f.queue.List(context.Background(), p)
	if err != nil {
		t.Fatalf("List(%+v): %v", p, err)
	}
	return got
}

// ids reduces summaries to their canonical addresses.
func ids(in []memory.CandidateSummary) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, c.ID)
	}
	return out
}

// treeDigest hashes every file under root, path and contents, so a test
// can assert that an operation left the store BYTE-IDENTICAL rather than
// merely "looking the same".
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		sum := sha256.Sum256(data)
		lines = append(lines, rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(lines)
	whole := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(whole[:])
}
