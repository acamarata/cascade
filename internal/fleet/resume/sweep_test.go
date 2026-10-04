package resume

// Purpose: P1-CORE-19 acceptance for Sweep (sweep.go): final-marked
//   residue removal, the ttl backstop with its expired marker, claim
//   skipping, and the scope rule that leaves every foreign entity alone.
//   Every check reads stored state (journal heads, records), never events.
// SPORT: internal.fleet.resume.Sweep/ADDED (P1-CORE-19).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/testkit"
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
	if _, err := Sweep(ctx, f.deps, testInstant, sweepTTL); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n := f.records(t, "residue"); n != 0 {
		t.Fatalf("residue records after Sweep = %d, want 0", n)
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
	if !f.deps.Claims.TryClaim("claimed") {
		t.Fatal("TryClaim: want the claim")
	}
	if _, err := Sweep(ctx, f.deps, testInstant, sweepTTL); err != nil || f.state(t, "young").Final != "" {
		t.Fatalf("young unclaimed entity touched: err=%v", err)
	}
	late := testInstant.Add(sweepTTL + time.Second)
	if _, err := Sweep(ctx, f.deps, late, sweepTTL); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if st := f.state(t, "claimed"); st.Final != "" || f.head(t, FanOutEntity("claimed")) != 1 {
		t.Fatalf("claimed entity written: marker %q head %d", st.Final, f.head(t, FanOutEntity("claimed")))
	}
	if f.deps.Claims.TryClaim("claimed") {
		t.Fatal("Sweep released a claim it never took")
	}
	f.deps.Claims.Release("claimed")
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
