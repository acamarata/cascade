package cascadepa

// Purpose (this file): the clocks and the in-memory BridgeState fake every
//   bridge test in this package drives the real stores through.
//
// Constraints: the fake below is the ONLY in-memory BridgeState that exists.
//   Production has none by design — see state.go's header on why an in-process
//   map could not answer "does the pairing survive a restart?".
//
// SPORT: plugins/cascade-pa bridge-test-fakes/TEST (P1-E23-W5-S48-T1).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// fixedTestClock is a PairClock pinned to one instant.
type fixedTestClock struct{ at time.Time }

func (c fixedTestClock) Now() time.Time { return c.at }

// advancingClock moves the clock forward mid-scenario without racing a timer.
type advancingClock struct{ at time.Time }

func (c *advancingClock) Now() time.Time          { return c.at }
func (c *advancingClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func t0() time.Time { return time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC) }

// errStoreDown is the fake store's failure.
var errStoreDown = errors.New("test: bridge state store unreachable")

// memState is the in-memory BridgeState test fake.
type memState struct {
	mu       sync.Mutex
	rows     map[string]SubjectState
	loadErr  error
	saveErr  error
	saveCall int
}

func newMemState() *memState { return &memState{rows: map[string]SubjectState{}} }

func (m *memState) Load(_ context.Context, subject string) (SubjectState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return SubjectState{}, false, m.loadErr
	}
	st, ok := m.rows[subject]
	return st, ok, nil
}

// Save emulates the host store's COMPARE-AND-SWAP: a write whose Version is
// not the stored one is REFUSED, exactly as internal/bridge's real Save
// refuses it. A fake that accepted every write would make every concurrency
// test in this package pass by ignoring the property under test.
func (m *memState) Save(_ context.Context, st SubjectState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	cur, exists := m.rows[st.Subject]
	if exists != (st.Version != 0) || (exists && cur.Version != st.Version) {
		return ErrStateConflict
	}
	st.Version++
	m.rows[st.Subject] = st
	m.saveCall++
	return nil
}

// seed writes st ignoring the version, for a test that wants a starting row
// without performing a real read-modify-write first.
func (m *memState) seed(st SubjectState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st.Version == 0 {
		st.Version = 1
	}
	m.rows[st.Subject] = st
}

// fixedEntropy is a deterministic 5-byte entropy source.
func fixedEntropy() io.Reader { return bytes.NewReader([]byte{0x11, 0x22, 0x33, 0x44, 0x55}) }

const testSubject = "tg-testsubject00"

// syntheticCredential is the only "bot token" this package's tests handle. It
// is obviously not a real credential and is never sent anywhere.
const syntheticCredential = "111111:SYNTHETIC-TEST-TOKEN-NOT-REAL"

// testPairKey is the derived pairing-code key every store in these tests is
// built with — the real derivation, not a hand-rolled byte slice, so a change
// to DerivePairCodeKey cannot silently stop being exercised.
func testPairKey(t *testing.T) []byte {
	t.Helper()
	key, err := DerivePairCodeKey(syntheticCredential)
	if err != nil {
		t.Fatalf("DerivePairCodeKey: %v", err)
	}
	return key
}
