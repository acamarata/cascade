package resume

// Purpose: P1-CORE-19 acceptance for Sweep (sweep.go): final-marked
//   residue removal, the ttl backstop with its expired marker, claim
//   skipping (fan-outs and orphan request records), foreign entities left
//   alone; P1-CORE-15 attempt-slot retention (P1-BF-R13, widened by
//   P1-BF-R143). Every check reads stored state, never events.
// SPORT: internal.fleet.resume.Sweep/CHANGE (P1-CORE-15).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const sweepTTL = time.Hour

// fanFixture is one real SQLite store with the production FanOutStore.
type fanFixture struct {
	fs   *FanOutStore
	js   *journal.SQLiteStore
	raw  provider.Store
	deps FanOutDeps
}

func newFanFixture(t *testing.T) *fanFixture {
	t.Helper()
	js, raw, _ := newRealStore(t)
	sq := js.(*journal.SQLiteStore)
	fs, err := NewFanOutStore(js, raw)
	if err != nil {
		t.Fatalf("NewFanOutStore: %v", err)
	}
	return &fanFixture{fs: fs, js: sq, raw: raw,
		deps: FanOutDeps{Store: raw, Heads: sq, Claims: NewClaims(), Clock: testkit.NewFrozenClock(testInstant)}}
}

// seed writes a fan-out the way the producer does: cursor, optional
// request record, then an ok leg (start, record, done) per okLegs entry.
func (f *fanFixture) seed(t *testing.T, id string, legs int, record bool, okLegs ...int) {
	t.Helper()
	ctx, req := context.Background(), legReq()
	req.FanOut = legs
	if _, err := f.fs.WriteCursor(ctx, FanOutCursor{FanOutID: id, TaskID: req.TaskID, Legs: legs, RequestKey: id}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	if record {
		if err := f.fs.PutRequest(ctx, id, req); err != nil {
			t.Fatalf("PutRequest: %v", err)
		}
	}
	for _, leg := range okLegs {
		attempt, err := f.fs.AppendLeg(ctx, "fanout_leg_started", id, leg, nil)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		rec := conductor.LegResult{FanOutID: id, TaskID: req.TaskID, LegIndex: leg, Attempt: attempt, RequestDigest: "d"}
		if err := f.fs.PutLegResult(ctx, rec); err != nil {
			t.Fatalf("PutLegResult: %v", err)
		}
		fields := map[string]string{"attempt": itoa(attempt), "outcome": conductor.LegOutcomeOK, "result_key": conductor.LegResultKey(id, leg)}
		if _, err := f.fs.AppendLeg(ctx, "fanout_leg_done", id, leg, fields); err != nil {
			t.Fatalf("done: %v", err)
		}
	}
}

// records counts id's leg results and request record.
func (f *fanFixture) records(t *testing.T, id string) int {
	t.Helper()
	n := 0
	for _, ns := range []string{legResultsNamespace, requestsNamespace} {
		keys, err := listKeys(context.Background(), f.raw, ns)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			if k == id || len(k) > len(id) && k[:len(id)+1] == id+"#" {
				n++
			}
		}
	}
	return n
}

func (f *fanFixture) state(t *testing.T, id string) FanOutState {
	t.Helper()
	st, err := f.fs.State(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *fanFixture) head(t *testing.T, entity string) uint64 {
	t.Helper()
	h, err := f.js.HeadSeq(context.Background(), entity)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSweepDeletesFinalMarkedRecords(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	for _, outcome := range []string{OutcomeDelivered, OutcomeFailed, OutcomeCancelled, OutcomeTerminal, OutcomeUnknown} {
		id := "fin-" + outcome
		f.seed(t, id, 2, true, 0, 1)
		if err := f.fs.Finalize(ctx, id, outcome); err != nil {
			t.Fatalf("Finalize(%s): %v", outcome, err)
		}
		if n := f.records(t, id); n != 0 || f.state(t, id).Final != outcome {
			t.Fatalf("%s: %d records after finalize, marker %q; want 0 and the marker", outcome, n, f.state(t, id).Final)
		}
	}
	// Residue: a crash between the marker and DeleteTask.
	f.seed(t, "residue", 2, true, 0, 1)
	if err := appendFinal(ctx, f.fs.journal, "residue", OutcomeDelivered); err != nil {
		t.Fatal(err)
	}
	if f.records(t, "residue") != 3 {
		t.Fatal("seed: want 2 leg results and the request record before the sweep")
	}
	if _, err := Sweep(ctx, f.deps, testInstant, sweepTTL); err != nil || f.records(t, "residue") != 0 {
		t.Fatalf("Sweep: %v, residue records %d; want 0", err, f.records(t, "residue"))
	}
}

func TestSweepBackstopDeletesUnmarked(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	f.seed(t, "truncated", 2, false) // cursor, no marker, no record
	f.seed(t, "exhausted", 1, true)
	for i := 0; i < MaxLegStarts; i++ {
		if _, err := f.fs.AppendLeg(ctx, "fanout_leg_started", "exhausted", 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	young := newFanFixture(t)
	young.seed(t, "young", 1, true)
	if _, err := Sweep(ctx, young.deps, testInstant.Add(time.Minute), sweepTTL); err != nil || young.records(t, "young") != 1 || young.state(t, "young").Final != "" {
		t.Fatalf("young entity touched: err=%v records=%d marker=%q", err, young.records(t, "young"), young.state(t, "young").Final)
	}
	expired, err := Sweep(ctx, f.deps, testInstant.Add(sweepTTL+time.Second), sweepTTL)
	if err != nil || len(expired) != 2 {
		t.Fatalf("Sweep = (%v, %v), want both entities expired", expired, err)
	}
	for _, id := range []string{"truncated", "exhausted"} {
		if n, st := f.records(t, id), f.state(t, id); n != 0 || st.Final != OutcomeExpired {
			t.Fatalf("%s: records %d, marker %q; want 0 and expired", id, n, st.Final)
		}
	}
}

func TestSweepSkipsInFlight(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	f.seed(t, "claimed", 1, false)
	f.seed(t, "young", 1, false)
	if err := f.fs.PutRequest(ctx, "orphan", legReq()); err != nil { // a request record with no entity
		t.Fatal(err)
	}
	if !f.deps.Claims.TryClaim("claimed") || !f.deps.Claims.TryClaim("orphan") {
		t.Fatal("TryClaim: want both claims")
	}
	if _, err := Sweep(ctx, f.deps, testInstant, sweepTTL); err != nil || f.state(t, "young").Final != "" {
		t.Fatalf("young unclaimed entity touched: err=%v", err)
	}
	late := testInstant.Add(sweepTTL + time.Second)
	if _, err := Sweep(ctx, f.deps, late, sweepTTL); err != nil || f.state(t, "claimed").Final != "" || f.head(t, FanOutEntity("claimed")) != 1 || f.records(t, "orphan") != 1 {
		t.Fatalf("Sweep %v; claimed ids written: head %d, orphan records %d", err, f.head(t, FanOutEntity("claimed")), f.records(t, "orphan"))
	}
	if f.deps.Claims.TryClaim("claimed") || f.deps.Claims.TryClaim("orphan") {
		t.Fatal("Sweep released a claim it never took")
	}
	f.deps.Claims.Release("claimed")
	f.deps.Claims.Release("orphan")
	if _, err := Sweep(ctx, f.deps, late, sweepTTL); err != nil || f.records(t, "orphan") != 0 {
		t.Fatalf("released orphan: err %v, records %d; want it reaped", err, f.records(t, "orphan"))
	}
	if _, err := Sweep(ctx, FanOutDeps{Store: f.raw, Heads: f.js, Clock: f.deps.Clock}, late, sweepTTL); err != ErrConstructionFailed {
		t.Fatalf("Sweep(nil claims) = %v, want ErrConstructionFailed", err)
	}
	if _, err := Scan(ctx, FanOutDeps{Store: f.raw, Heads: f.js, Clock: f.deps.Clock}); err != ErrConstructionFailed {
		t.Fatalf("Scan(nil claims) = %v, want ErrConstructionFailed", err)
	}
	var nilClaims *Claims
	if nilClaims.TryClaim("x") {
		t.Fatal("TryClaim on a nil table returned true")
	}
}

// slots lists id's conductor.fanout.attempts keys.
func (f *fanFixture) slots(t *testing.T, id string) []string {
	t.Helper()
	keys, err := listKeys(context.Background(), f.raw, legAttemptsNamespace)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range keys {
		if len(k) > len(id) && k[:len(id)+1] == id+"#" {
			out = append(out, k)
		}
	}
	return out
}

func TestSweepDeletesTerminalAttemptSlots(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	f.seed(t, "slots-final", 2, true, 0, 1)
	f.seed(t, "slots-expired", 1, true, 0)
	if err := appendFinal(ctx, f.fs.journal, "slots-final", OutcomeDelivered); err != nil {
		t.Fatal(err)
	}
	if len(f.slots(t, "slots-final")) != 2 || len(f.slots(t, "slots-expired")) != 1 {
		t.Fatal("seed: want one attempt slot per finished leg before the sweep")
	}
	_, err := Sweep(ctx, f.deps, testInstant.Add(sweepTTL+time.Second), sweepTTL)
	for _, id := range []string{"slots-final", "slots-expired"} {
		if got, n := f.slots(t, id), f.records(t, id); err != nil || len(got) != 0 || n != 0 {
			t.Fatalf("%s after Sweep (%v): slots %v, records %d; want both gone", id, err, got, n)
		}
	}
}

func TestSweepKeepsInFlightAttemptSlots(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	f.seed(t, "inflight", 2, true, 0) // leg 0 finished; leg 1 started three times, no result
	for i := 0; i < MaxLegStarts; i++ {
		if _, err := f.fs.AppendLeg(ctx, "fanout_leg_started", "inflight", 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Sweep(ctx, f.deps, testInstant.Add(sweepTTL+time.Second), sweepTTL) // expires it: no marker read back yet
	want := []string{"inflight#1#1", "inflight#1#2", "inflight#1#3"}
	if got := f.slots(t, "inflight"); err != nil || len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("slots after Sweep (%v) = %v, want leg 1's three kept and leg 0's gone %v", err, got, want)
	}
	if _, err := f.fs.claimAttempt(ctx, "inflight", 1, "d"); err != ErrLegAttemptsExhausted { //nolint:errorlint // identity
		t.Fatalf("fourth raw start after Sweep = %v, want ErrLegAttemptsExhausted", err)
	}
}

func TestSweepDeletesFinalizedUnfinishedLegSlots(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	f.seed(t, "fin-open", 2, true, 0) // leg 0 finished; leg 1 started below, no result
	if _, err := f.fs.AppendLeg(ctx, "fanout_leg_started", "fin-open", 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.fs.Finalize(ctx, "fin-open", OutcomeCancelled); err != nil || len(f.slots(t, "fin-open")) != 1 {
		t.Fatalf("Finalize: %v, slots %v; want leg 1's slot kept by the leg-result gate", err, f.slots(t, "fin-open"))
	}
	if _, err := Sweep(ctx, f.deps, testInstant, sweepTTL); err != nil || len(f.slots(t, "fin-open")) != 0 || f.records(t, "fin-open") != 0 {
		t.Fatalf("Sweep: %v, slots %v, records %d; want none once the marker is read back", err, f.slots(t, "fin-open"), f.records(t, "fin-open"))
	}
}

// slotFaultStore fails the leg-result read inside a transaction.
type slotFaultStore struct {
	provider.Store
	err error
}

type slotFaultTx struct {
	provider.Tx
	err error
}

func (s slotFaultStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	return s.Store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error { return fn(ctx, slotFaultTx{Tx: tx, err: s.err}) })
}

func (x slotFaultTx) Get(ctx context.Context, ns, key string) ([]byte, error) {
	if ns == legResultsNamespace {
		return nil, x.err
	}
	return x.Tx.Get(ctx, ns, key)
}

func TestSweepAttemptSlotStoreErrorFailsClosed(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	f.seed(t, "slots-err", 2, true, 0, 1) // no marker yet: the expiry's deletes take the leg-result gate
	injected := errors.New("injected: leg result read failed")
	deps := FanOutDeps{Store: slotFaultStore{Store: f.raw, err: injected}, Heads: f.deps.Heads, Claims: f.deps.Claims, Clock: f.deps.Clock}
	if _, err := Sweep(ctx, deps, testInstant.Add(sweepTTL+time.Second), sweepTTL); !errors.Is(err, injected) || !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("Sweep = %v, want the injected read error returned", err)
	}
	if got := f.slots(t, "slots-err"); len(got) != 2 {
		t.Fatalf("slots after a failed read = %v, want both kept", got)
	}
	if _, found, _ := f.fs.GetLegResult(ctx, "slots-err", 0); !found {
		t.Fatal("leg result deleted after a failed slot read, want it kept")
	}
}
