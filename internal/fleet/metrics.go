// Purpose: the two fleet supervision measurements S-40.T1's TUI and
//
//	S-40.T4's RPC read — how often a task needed a human, and how often
//	tier-1 auto-advance carried one through without needing one.
//
// WHY THE SHAPE DIFFERS FROM THE CONTRACT'S. The contract asks for
//
//	map[TaskID]uint64 registered with the counters/gauges snapshot API.
//	That API is runtime.Registry, and RegisterCounter PANICS on a
//	duplicate name by design — a duplicate registration is a startup
//	programming error there, deliberately not a runtime condition. One
//	counter per task id would therefore panic the daemon the second time a
//	task id repeated, and grow the registry without bound before it did.
//	Unbounded metric cardinality keyed by a per-task identifier is the
//	classic form of this mistake. R-14.247 §10 rules the corrected shape,
//	which is what this file implements:
//
//	  - ONE registry counter for the fleet-wide interruption total, which
//	    is what a snapshot consumer can actually display;
//	  - ONE registry counter per auto-advance-ELIGIBLE rung, registered
//	    once at construction. That is exactly two (L0 and L1 are the only
//	    eligible rungs, 06-FORGE-SPEC §5.15), so the cardinality is bounded
//	    by the enum rather than by traffic;
//	  - the per-task breakdown kept INSIDE this package as a bounded map
//	    that never reaches the registry.
//
// Inputs: a runtime.Registry to register against.
// Outputs: monotonic counters, and a per-task view for the TUI.
// Constraints: counters are strictly monotonic within a daemon session and
//
//	are never reset. The per-task map is BOUNDED and evicts oldest-first,
//	because a daemon that runs for a month would otherwise accumulate one
//	entry for every task it ever saw. Eviction loses the oldest task's
//	breakdown; it never decrements a total, so the monotonic invariant
//	holds on the numbers anyone aggregates.
//
// SPORT: fleet.Metrics/ADDED (P1-E18-W4-S40-T3). The contract names the
//
//	type FleetMetrics; the lint wall's stutter rule refuses
//	fleet.FleetMetrics, so it is Metrics. The REGISTERED METRIC NAMES the
//	contract fixes are unchanged, which is the part a snapshot consumer
//	reads.

package fleet

import (
	"sync"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The registered metric names. Stable: a snapshot consumer reads these by
// name, so renaming one is a breaking change to S-40.T4's RPC surface.
const (
	// MetricInterruptionsTotal counts every promotion of a task to
	// human-attention status, fleet-wide.
	MetricInterruptionsTotal = "fleet_interruptions_total"
	// MetricAutoResolvedTotal counts tier-1 auto-advance resolutions. It
	// carries a `level` label, one registered counter per eligible rung.
	MetricAutoResolvedTotal = "fleet_auto_resolved_total"
	// MetricAutoResolvedIneligible counts resolutions reported at a rung
	// that is NOT auto-advance eligible. It exists so such a report is
	// VISIBLE rather than dropped: a non-zero value here means something
	// upstream resolved a rung it should not have, which is a defect worth
	// seeing and not worth panicking over.
	MetricAutoResolvedIneligible = "fleet_auto_resolved_ineligible_total"
)

// autoAdvanceEligibleLevels are the only rungs tier-1 auto-advance may
// resolve (06-FORGE-SPEC §5.15). The counter set is registered from this
// list, so its cardinality is the enum's and not the traffic's.
var autoAdvanceEligibleLevels = [...]policy.RiskLevel{policy.L0, policy.L1}

// maxTrackedTasks bounds the per-task breakdown. It is a display aid for
// S-40.T1's TUI, which shows a screenful; a month-long daemon must not
// hold one entry per task it ever saw. The fleet-wide total is unaffected
// by eviction, so the number anyone aggregates stays whole.
const maxTrackedTasks = 1024

// Metrics holds the supervision counters for one daemon session.
//
// The registry counters are atomic and lock-free. The per-task map and its
// eviction order are guarded by one mutex, which is the only contended
// path and is held for O(1) work.
type Metrics struct {
	interruptions *runtime.Counter
	autoResolved  map[policy.RiskLevel]*runtime.Counter
	ineligible    *runtime.Counter

	mu      sync.Mutex
	perTask map[string]uint64
	// order is the insertion order of perTask's keys, used for
	// oldest-first eviction. A task already tracked is not re-appended, so
	// a hot task does not push everything else out.
	order []string
}

// NewMetrics registers the counters and returns the metric set.
//
// The registry is required: a metric set with nowhere to register would
// count into a void while reporting itself as wired, and the snapshot
// consumers would show zeros with no way to tell that from a quiet fleet.
func NewMetrics(reg *runtime.Registry) (*Metrics, error) {
	if reg == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"fleet: metrics need a registry to register against")
	}
	m := &Metrics{
		interruptions: reg.RegisterCounter(MetricInterruptionsTotal, nil),
		autoResolved:  make(map[policy.RiskLevel]*runtime.Counter, len(autoAdvanceEligibleLevels)),
		ineligible:    reg.RegisterCounter(MetricAutoResolvedIneligible, nil),
		perTask:       make(map[string]uint64),
	}
	for _, level := range autoAdvanceEligibleLevels {
		m.autoResolved[level] = reg.RegisterCounter(
			MetricAutoResolvedTotal+"_"+level.String(),
			map[string]string{"level": level.String()},
		)
	}
	return m, nil
}

// RecordInterruption counts one promotion of taskID to human-attention
// status.
//
// An empty taskID still counts toward the fleet-wide total and is not
// tracked per task: the interruption happened, and dropping it from the
// total because the source did not name a task would under-report the
// number an operator acts on.
func (m *Metrics) RecordInterruption(taskID string) {
	m.interruptions.Inc()
	if taskID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, tracked := m.perTask[taskID]; !tracked {
		m.evictIfFullLocked()
		m.order = append(m.order, taskID)
	}
	m.perTask[taskID]++
}

// evictIfFullLocked drops the oldest tracked task when the map is at its
// bound. Caller holds mu.
func (m *Metrics) evictIfFullLocked() {
	for len(m.perTask) >= maxTrackedTasks && len(m.order) > 0 {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.perTask, oldest)
	}
}

// RecordAutoResolved counts one tier-1 auto-advance resolution at level.
//
// A rung that is not auto-advance eligible is counted separately rather
// than dropped or panicked on. Dropping it would hide a real upstream
// defect — something resolved a rung it should not have — and panicking
// would take the daemon down over a metric.
func (m *Metrics) RecordAutoResolved(level policy.RiskLevel) {
	if c, eligible := m.autoResolved[level]; eligible {
		c.Inc()
		return
	}
	m.ineligible.Inc()
}

// Interruptions returns the fleet-wide interruption total.
func (m *Metrics) Interruptions() int64 { return m.interruptions.Value() }

// InterruptionsFor returns the count for one task, and whether that task
// is still tracked. A task evicted by the bound reports (0, false), which
// a caller can tell apart from a task that was never interrupted — the
// second answers (0, false) as well, and both mean "nothing to show",
// which is the only thing a display can do about either.
func (m *Metrics) InterruptionsFor(taskID string) (uint64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, tracked := m.perTask[taskID]
	return n, tracked
}

// AutoResolved returns the count for one rung. An ineligible rung reports
// the ineligible tally, so a caller asking about L4 is told what was
// actually recorded rather than a zero that looks like silence.
func (m *Metrics) AutoResolved(level policy.RiskLevel) int64 {
	if c, eligible := m.autoResolved[level]; eligible {
		return c.Value()
	}
	return m.ineligible.Value()
}

// TrackedTasks returns a copy of the per-task breakdown, for the TUI.
//
// A copy rather than the map itself: handing out the live map would let a
// display iterate it while the bus consumer writes, and the race detector
// would be right to complain.
func (m *Metrics) TrackedTasks() map[string]uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]uint64, len(m.perTask))
	for k, v := range m.perTask {
		out[k] = v
	}
	return out
}
