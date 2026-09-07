package sessions_test

import (
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/tailer"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func fixedClock() *runtime.FixedClock {
	return runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
}

func recAt(state string, offsets ...func(*sessions.SessionRecord)) *sessions.SessionRecord {
	rec := &sessions.SessionRecord{SessionID: "s1", State: state}
	for _, f := range offsets {
		f(rec)
	}
	return rec
}

// TestAdvanceFailClosedZeroObservation proves the fully zero-value
// Observation classifies (StateUnknown, ConfidenceNone), never a panic.
// TestAdvanceFailClosedNilClock proves the same for a nil clock.
func TestAdvanceFailClosedZeroObservation(t *testing.T) {
	m := sessions.NewStateMachine()
	st, conf := m.Advance(sessions.Observation{}, fixedClock())
	if st != sessions.StateUnknown || conf != sessions.ConfidenceNone {
		t.Fatalf("Advance(zero Observation) = (%s, %v), want (unknown, 0.0)", st, conf)
	}
}

func TestAdvanceFailClosedNilClock(t *testing.T) {
	m := sessions.NewStateMachine()
	st, conf := m.Advance(sessions.Observation{Census: &census.Snapshot{Pid: 1}}, nil)
	if st != sessions.StateUnknown || conf != sessions.ConfidenceNone {
		t.Fatalf("Advance(nil clock) = (%s, %v), want (unknown, 0.0)", st, conf)
	}
}

// TestAdvanceTerminalStateNeverLeavesClosed: no signal combination moves
// a StateClosed session anywhere else.
func TestAdvanceTerminalStateNeverLeavesClosed(t *testing.T) {
	m := sessions.NewStateMachine()
	clk := fixedClock()
	now := clk.Now().UnixMilli()
	closed := recAt(sessions.StateClosed.String(), func(r *sessions.SessionRecord) {
		r.LastPromptAt, r.LastToolAt = &now, &now
		r.ToolCount = 5
	})
	obs := sessions.Observation{Census: &census.Snapshot{Pid: 42}, Domain: closed}
	st, conf := m.Advance(obs, clk)
	if st != sessions.StateClosed {
		t.Fatalf("Advance from closed = %s, want closed (terminal)", st)
	}
	if conf != sessions.ConfidenceFull {
		t.Fatalf("Advance from closed confidence = %v, want ConfidenceFull", conf)
	}
}

// TestAdvanceNoCycleCanSpinForever: repeated advancing under a fixed
// clock never grows the trajectory past the six-state universe.
func TestAdvanceNoCycleCanSpinForever(t *testing.T) {
	m := sessions.NewStateMachine()
	clk := fixedClock()
	obs := sessions.Observation{Census: &census.Snapshot{Pid: 7}, Domain: recAt(sessions.StateActive.String())}
	seen := map[sessions.SessionState]bool{}
	st, _ := m.Advance(obs, clk)
	for i := 0; i < 100; i++ {
		obs.Domain = recAt(st.String())
		next, _ := m.Advance(obs, clk)
		seen[next] = true
		if next != st && len(seen) > 6 {
			t.Fatalf("state trajectory grew past the six-state universe: %v", seen)
		}
		st = next
	}
}

// TestAdvanceRefusesImpossibleTransition: an impossible candidate
// (Stalled -> Idle is not an edge) is REFUSED - Advance returns the
// prior state unchanged, never applied and never dropped to a default.
func TestAdvanceRefusesImpossibleTransition(t *testing.T) {
	m := sessions.NewStateMachine()
	clk := fixedClock()
	idleElapsed := clk.Now().Add(-3 * time.Minute).UnixMilli()
	obs := sessions.Observation{
		Census: &census.Snapshot{Pid: 1},
		Domain: recAt(sessions.StateStalled.String(), func(r *sessions.SessionRecord) { r.LastToolAt = &idleElapsed }),
	}
	st, _ := m.Advance(obs, clk)
	if st != sessions.StateStalled {
		t.Fatalf("Advance(stalled -> idle-candidate) = %s, want stalled (refused, stayed put)", st)
	}
}

type transitionArc struct {
	name string
	from sessions.SessionState
	obs  func() sessions.Observation
	want sessions.SessionState
}

// transitionArcCases enumerates every reachable transition-table edge.
func transitionArcCases(clk *runtime.FixedClock) []transitionArc {
	now := clk.Now().UnixMilli()
	idleAt := clk.Now().Add(-5 * time.Minute).UnixMilli()
	blockedAt := clk.Now().Add(-15 * time.Minute).UnixMilli()
	stalledAt := clk.Now().Add(-40 * time.Minute).UnixMilli()
	withCensus := func(state string, set func(*sessions.SessionRecord)) sessions.Observation {
		return sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: recAt(state, set)}
	}
	noCensus := func(state string) sessions.Observation {
		return sessions.Observation{Domain: recAt(state)}
	}
	return []transitionArc{
		{"unknown to active", sessions.StateUnknown, func() sessions.Observation {
			return withCensus(sessions.StateUnknown.String(), func(r *sessions.SessionRecord) { r.LastPromptAt = &now })
		}, sessions.StateActive},
		{"active to idle after idleThreshold", sessions.StateActive, func() sessions.Observation {
			return withCensus(sessions.StateActive.String(), func(r *sessions.SessionRecord) { r.LastToolAt = &idleAt })
		}, sessions.StateIdle},
		{"idle to blocked after blockedThreshold", sessions.StateIdle, func() sessions.Observation {
			return withCensus(sessions.StateIdle.String(), func(r *sessions.SessionRecord) { r.LastToolAt = &blockedAt })
		}, sessions.StateBlocked},
		{"blocked to stalled after stalledThreshold", sessions.StateBlocked, func() sessions.Observation {
			return withCensus(sessions.StateBlocked.String(), func(r *sessions.SessionRecord) { r.LastToolAt = &stalledAt })
		}, sessions.StateStalled},
		{"stalled back to active", sessions.StateStalled, func() sessions.Observation {
			return withCensus(sessions.StateStalled.String(), func(r *sessions.SessionRecord) { r.LastPromptAt = &now })
		}, sessions.StateActive},
		{"idle to closed when process disappears", sessions.StateIdle, func() sessions.Observation {
			return noCensus(sessions.StateIdle.String())
		}, sessions.StateClosed},
		{"blocked to closed when process disappears", sessions.StateBlocked, func() sessions.Observation {
			return noCensus(sessions.StateBlocked.String())
		}, sessions.StateClosed},
	}
}

// TestAdvanceTransitionArcs walks every edge, checked against an
// independently re-derived set (allowedForTest), not the same table twice.
func TestAdvanceTransitionArcs(t *testing.T) {
	clk := fixedClock()
	m := sessions.NewStateMachine()
	for _, tc := range transitionArcCases(clk) {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := m.Advance(tc.obs(), clk)
			if got != tc.want {
				t.Fatalf("Advance(%s) = %s, want %s", tc.name, got, tc.want)
			}
			if !allowedForTest(tc.from, tc.want) {
				t.Fatalf("arc %s -> %s is not in the declared transition table", tc.from, tc.want)
			}
		})
	}
}

func allowedForTest(from, to sessions.SessionState) bool {
	table := map[sessions.SessionState]map[sessions.SessionState]bool{
		sessions.StateUnknown: {sessions.StateUnknown: true, sessions.StateActive: true, sessions.StateClosed: true},
		sessions.StateActive:  {sessions.StateActive: true, sessions.StateIdle: true, sessions.StateBlocked: true, sessions.StateClosed: true},
		sessions.StateIdle:    {sessions.StateIdle: true, sessions.StateActive: true, sessions.StateBlocked: true, sessions.StateStalled: true, sessions.StateClosed: true},
		sessions.StateBlocked: {sessions.StateBlocked: true, sessions.StateActive: true, sessions.StateStalled: true, sessions.StateClosed: true},
		sessions.StateStalled: {sessions.StateStalled: true, sessions.StateActive: true, sessions.StateClosed: true},
		sessions.StateClosed:  {sessions.StateClosed: true},
	}
	return table[from][to]
}

// TestAdvanceDeterministic: identical inputs produce identical outputs.
func TestAdvanceDeterministic(t *testing.T) {
	m := sessions.NewStateMachine()
	clk := fixedClock()
	now := clk.Now().UnixMilli()
	obs := sessions.Observation{
		Census: &census.Snapshot{Pid: 1},
		Domain: recAt(sessions.StateActive.String(), func(r *sessions.SessionRecord) { r.LastPromptAt = &now }),
	}
	st1, conf1 := m.Advance(obs, clk)
	for i := 0; i < 10; i++ {
		st2, conf2 := m.Advance(obs, clk)
		if st1 != st2 || conf1 != conf2 {
			t.Fatalf("Advance non-deterministic at call %d: (%s,%v) vs (%s,%v)", i, st1, conf1, st2, conf2)
		}
	}
}

// TestAdvanceConcurrentCallsNeverLoseOrDoubleApply: many goroutines over
// the same Observation/clock (StateMachine holds no mutable state) all
// match the single-goroutine result under -race.
func TestAdvanceConcurrentCallsNeverLoseOrDoubleApply(t *testing.T) {
	m := sessions.NewStateMachine()
	clk := fixedClock()
	now := clk.Now().UnixMilli()
	obs := sessions.Observation{
		Census: &census.Snapshot{Pid: 9},
		Domain: recAt(sessions.StateActive.String(), func(r *sessions.SessionRecord) { r.LastToolAt = &now }),
	}
	wantState, wantConf := m.Advance(obs, clk)
	const n = 64
	var wg sync.WaitGroup
	results := make([]sessions.SessionState, n)
	confs := make([]sessions.ConfidenceScore, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], confs[i] = m.Advance(obs, clk)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if results[i] != wantState || confs[i] != wantConf {
			t.Fatalf("goroutine %d: Advance = (%s,%v), want (%s,%v)", i, results[i], confs[i], wantState, wantConf)
		}
	}
}

// degradedCase: base/degraded classify identically; degraded scores
// strictly lower confidence.
type degradedCase struct {
	name           string
	base, degraded sessions.Observation
}

// TestAdvanceDegradedSignalsNeverPanic: truncated transcript, closed SSE
// stream, and "nothing read this cycle" each degrade confidence, never panic.
func TestAdvanceDegradedSignalsNeverPanic(t *testing.T) {
	clk := fixedClock()
	now := clk.Now().UnixMilli()
	active := func() *sessions.SessionRecord {
		return recAt(sessions.StateActive.String(), func(r *sessions.SessionRecord) { r.LastPromptAt = &now })
	}
	parseErr := &tailer.ParseError{Line: 3, ByteLen: 128, Cause: cascade.New(cascade.KindInvalidInput, "malformed transcript line")}
	cases := []degradedCase{
		{"truncated transcript event",
			sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: active(), Transcript: &tailer.Record{Tool: "read"}},
			sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: active(), TranscriptErr: parseErr}},
		{"daemon-closed SSE stream",
			sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: active()},
			sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: active(), SSEClosed: true}},
		{"nothing read this cycle",
			sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: active(), Transcript: &tailer.Record{Tool: "write"}},
			sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: active()}},
	}
	m := sessions.NewStateMachine()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stBase, confBase := m.Advance(tc.base, clk)
			stDeg, confDeg := m.Advance(tc.degraded, clk)
			if stBase != sessions.StateActive || stDeg != sessions.StateActive {
				t.Fatalf("classification changed: base=%s degraded=%s, want both active", stBase, stDeg)
			}
			if confDeg >= confBase {
				t.Fatalf("degraded confidence %v not lower than base confidence %v", confDeg, confBase)
			}
		})
	}
}

// TestAdvanceClassificationEdgeCases: (1) a non-nil zero-value
// census.Snapshot is still "process present"; (2) an unrecognized
// Domain.State is StateUnknown, not a panic, and Unknown->Active still
// fires on fresh activity; (3) no activity timestamps is Idle not
// Unknown (started from Active - Unknown->Idle is not a permitted edge).
func TestAdvanceClassificationEdgeCases(t *testing.T) {
	m := sessions.NewStateMachine()
	clk := fixedClock()
	now := clk.Now().UnixMilli()

	unparseablePrior := recAt("some-future-corpus-label-nobody-registered-yet", func(r *sessions.SessionRecord) { r.LastPromptAt = &now })
	cases := []struct {
		name string
		obs  sessions.Observation
		want sessions.SessionState
	}{
		{"empty but non-nil census snapshot", sessions.Observation{Census: &census.Snapshot{}, Domain: recAt(sessions.StateIdle.String())}, sessions.StateIdle},
		{"unparseable prior state with fresh activity", sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: unparseablePrior}, sessions.StateActive},
		{"no activity timestamps is idle not unknown", sessions.Observation{Census: &census.Snapshot{Pid: 1}, Domain: recAt(sessions.StateActive.String())}, sessions.StateIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if st, _ := m.Advance(tc.obs, clk); st != tc.want {
				t.Fatalf("Advance(%s) = %s, want %s", tc.name, st, tc.want)
			}
		})
	}
}
