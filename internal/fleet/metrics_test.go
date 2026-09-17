package fleet

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the two supervision measurements, and the three
//   properties that make them trustworthy — they are monotonic, they are
//   BOUNDED, and they count each event exactly once even when the bus
//   redelivers it.
//
// The bound is the part worth testing hardest. The contract asked for a
//   counter per task id registered with the snapshot API; that API panics
//   on a duplicate name and grows without limit, so the corrected design
//   (R-14.247 §10) keeps the per-task view inside this package with an
//   explicit cap. A cap nobody tests is a cap nobody has.
// SPORT: fleet.Metrics tests (ADD) — P1-E18-W4-S40-T3.

// newMetrics builds a metric set over a fresh registry.
func newMetrics(t *testing.T) *Metrics {
	t.Helper()
	m, err := NewMetrics(runtime.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestFleetMetricsCountInterruptions covers the fleet-wide total and the
// per-task breakdown together, because a display shows both and they must
// agree.
func TestFleetMetricsCountInterruptions(t *testing.T) {
	m := newMetrics(t)
	m.RecordInterruption("task-a")
	m.RecordInterruption("task-a")
	m.RecordInterruption("task-b")

	if got := m.Interruptions(); got != 3 {
		t.Errorf("fleet total = %d, want 3", got)
	}
	if n, tracked := m.InterruptionsFor("task-a"); !tracked || n != 2 {
		t.Errorf("task-a = (%d, %v), want (2, true)", n, tracked)
	}
	if n, tracked := m.InterruptionsFor("task-b"); !tracked || n != 1 {
		t.Errorf("task-b = (%d, %v), want (1, true)", n, tracked)
	}
	if n, tracked := m.InterruptionsFor("never-seen"); tracked || n != 0 {
		t.Errorf("an untouched task = (%d, %v), want (0, false)", n, tracked)
	}
}

// TestAnUnnamedInterruptionStillCountsTowardTheTotal holds the choice made
// where a source does not name a task. Dropping it would under-report the
// number an operator acts on, so it counts fleet-wide and is simply not
// broken out.
func TestAnUnnamedInterruptionStillCountsTowardTheTotal(t *testing.T) {
	m := newMetrics(t)
	m.RecordInterruption("")
	if got := m.Interruptions(); got != 1 {
		t.Errorf("fleet total = %d, want 1", got)
	}
	if len(m.TrackedTasks()) != 0 {
		t.Errorf("an unnamed interruption was tracked per task: %v", m.TrackedTasks())
	}
}

// TestThePerTaskMapIsBounded is the property the corrected design exists
// for. A daemon that ran for a month would otherwise hold one entry for
// every task it ever saw.
func TestThePerTaskMapIsBounded(t *testing.T) {
	m := newMetrics(t)
	const overflow = maxTrackedTasks + 50
	for i := 0; i < overflow; i++ {
		m.RecordInterruption(fmt.Sprintf("task-%05d", i))
	}
	if got := len(m.TrackedTasks()); got > maxTrackedTasks {
		t.Fatalf("tracked %d tasks, above the bound of %d", got, maxTrackedTasks)
	}
	// Eviction is oldest-first, so the newest task is still there and the
	// oldest is gone.
	if _, tracked := m.InterruptionsFor(fmt.Sprintf("task-%05d", overflow-1)); !tracked {
		t.Error("the newest task was evicted; eviction is not oldest-first")
	}
	if _, tracked := m.InterruptionsFor("task-00000"); tracked {
		t.Error("the oldest task survived the bound")
	}
	// The TOTAL is untouched by eviction: the number anyone aggregates
	// stays whole even though the breakdown does not.
	if got := m.Interruptions(); got != int64(overflow) {
		t.Errorf("fleet total = %d after eviction, want %d — eviction must never decrement a total",
			got, overflow)
	}
}

// TestAHotTaskDoesNotEvictEverythingElse holds the second half of the
// eviction rule: a task already tracked is not re-appended to the order,
// so one noisy task cannot push the rest of the fleet out of the view.
func TestAHotTaskDoesNotEvictEverythingElse(t *testing.T) {
	m := newMetrics(t)
	m.RecordInterruption("quiet")
	for i := 0; i < maxTrackedTasks*2; i++ {
		m.RecordInterruption("hot")
	}
	if _, tracked := m.InterruptionsFor("quiet"); !tracked {
		t.Error("a hot task evicted a quiet one by repeating")
	}
	if len(m.TrackedTasks()) != 2 {
		t.Errorf("tracked %d tasks, want 2", len(m.TrackedTasks()))
	}
}

// TestAutoResolvedCountsPerEligibleRung covers the two rungs tier-1 may
// resolve, and the third case the contract names: an unexpected rung must
// create an entry rather than panic.
func TestAutoResolvedCountsPerEligibleRung(t *testing.T) {
	m := newMetrics(t)
	m.RecordAutoResolved(policy.L0)
	m.RecordAutoResolved(policy.L0)
	m.RecordAutoResolved(policy.L1)

	if got := m.AutoResolved(policy.L0); got != 2 {
		t.Errorf("L0 = %d, want 2", got)
	}
	if got := m.AutoResolved(policy.L1); got != 1 {
		t.Errorf("L1 = %d, want 1", got)
	}
	// An ineligible rung is counted separately rather than dropped or
	// panicked on: a non-zero value here means something upstream resolved
	// a rung it should not have, which is worth seeing and not worth
	// taking the daemon down over.
	for _, level := range []policy.RiskLevel{policy.L2, policy.L3, policy.L4} {
		m.RecordAutoResolved(level)
	}
	if got := m.AutoResolved(policy.L4); got != 3 {
		t.Errorf("ineligible tally = %d, want 3", got)
	}
}

// TestZeroStateReadsZeroWithoutPanicking is the contract's own
// zero-state case: a snapshot taken before anything happened answers, and
// answers zero.
func TestZeroStateReadsZeroWithoutPanicking(t *testing.T) {
	m := newMetrics(t)
	if got := m.Interruptions(); got != 0 {
		t.Errorf("fleet total = %d, want 0", got)
	}
	for _, level := range []policy.RiskLevel{policy.L0, policy.L1, policy.L2, policy.L3, policy.L4} {
		if got := m.AutoResolved(level); got != 0 {
			t.Errorf("%v = %d, want 0", level, got)
		}
	}
	if n, tracked := m.InterruptionsFor("anything"); tracked || n != 0 {
		t.Errorf("unknown task = (%d, %v), want (0, false)", n, tracked)
	}
	if len(m.TrackedTasks()) != 0 {
		t.Error("the zero state tracked a task")
	}
}

// TestConcurrentIncrementsAreSafe is the -race case the contract asks for.
// Both counter paths are driven from many goroutines at once, and the
// totals must come out exact rather than merely non-zero.
func TestConcurrentIncrementsAreSafe(t *testing.T) {
	m := newMetrics(t)
	const workers, each = 16, 64

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				m.RecordInterruption(fmt.Sprintf("task-%d", w))
				m.RecordAutoResolved(policy.L0)
			}
		}(w)
	}
	// Readers run concurrently too: a display polling while the fleet
	// works is the normal case, and handing out the live map would be a
	// race the detector would be right about.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_ = m.TrackedTasks()
				_ = m.Interruptions()
			}
		}()
	}
	wg.Wait()

	if got := m.Interruptions(); got != workers*each {
		t.Errorf("fleet total = %d, want %d", got, workers*each)
	}
	if got := m.AutoResolved(policy.L0); got != workers*each {
		t.Errorf("L0 = %d, want %d", got, workers*each)
	}
	for w := 0; w < workers; w++ {
		if n, _ := m.InterruptionsFor(fmt.Sprintf("task-%d", w)); n != each {
			t.Errorf("task-%d = %d, want %d", w, n, each)
		}
	}
}

// TestTheCountersAreRegisteredUnderTheirStableNames is acceptance
// criterion 5: a snapshot consumer reads these by name without importing
// this package, so the names are the contract.
func TestTheCountersAreRegisteredUnderTheirStableNames(t *testing.T) {
	reg := runtime.NewRegistry()
	m, err := NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	m.RecordInterruption("task-a")
	m.RecordAutoResolved(policy.L1)

	snap := reg.Snapshot(time.Unix(0, 0))
	if len(snap) == 0 {
		t.Fatal("the registry snapshot is empty; nothing was registered")
	}
	want := map[string]int64{
		MetricInterruptionsTotal:                           1,
		MetricAutoResolvedTotal + "_" + policy.L1.String(): 1,
		MetricAutoResolvedTotal + "_" + policy.L0.String(): 0,
		MetricAutoResolvedIneligible:                       0,
	}
	got := map[string]int64{}
	for _, entry := range snap {
		got[entry.Name] = entry.Value
	}
	for name, value := range want {
		if have, present := got[name]; !present || have != value {
			t.Errorf("snapshot[%q] = (%d, present=%v), want %d", name, have, present, value)
		}
	}
}

// TestAMetricSetNeedsARegistry holds Art.1 at the constructor: a metric
// set with nowhere to register would count into a void while reporting
// itself as wired, and the snapshot would show zeros with no way to tell
// that from a quiet fleet.
func TestAMetricSetNeedsARegistry(t *testing.T) {
	got, err := NewMetrics(nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	if got != nil {
		t.Error("a refused build returned a metric set")
	}
}
