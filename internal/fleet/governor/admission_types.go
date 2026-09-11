// Package governor (admission_types.go) defines the admission controller's
// request/permit/config vocabulary and the throttle-stage seam S-26.T3
// installs into.
//
// Purpose: AdmissionRequest (what a caller wants to run), Permit (the
//
//	caller's receipt, released when the work finishes), AdmissionConfig
//	(the controller's tunables), ThrottleStage/StageProvider (the seam
//	S-26.T3's throttle ladder installs to gate Admit), and the internal
//	priority-queue waiter type admission.go's Admit/Drain/release drive.
//
// Inputs: none of these are constructed directly by a caller other than
//
//	AdmissionRequest and AdmissionConfig, which callers build as plain
//	struct literals.
//
// Outputs: Permit is returned by a successful Admit and must be released
//
//	exactly once; a zero-value Permit is a safe no-op on Release.
//
// Constraints: no bare time.Now (SPORT clock injection carried by
//
//	admission.go); AdmissionRequest.Priority follows SamplerConfig.MaxHz's
//	own "zero means default" convention (R-14.11 style), not a magic
//	sentinel value.
//
// SPORT: internal/fleet/governor.AdmissionController (ADD, per T-2
//
//	sport_updates).
package governor

import (
	"container/heap"
	"sync"
	"time"
)

// PriorityDefault is the priority an AdmissionRequest with an unset
// (zero-value) Priority is treated as, on the 0-100 higher-first scale
// R-21.2 defines. Consumed by K/S-23.T2 and AC/S-59.T5.
const PriorityDefault = 50

// AdmissionRequest describes one caller's request to run a concurrently
// dispatched work item under the governor's admission gate.
type AdmissionRequest struct {
	// Kind labels the work item for observability, e.g. "compile",
	// "inference", "io", "generic", "model-call". Purely descriptive; it
	// does not affect admission decisions.
	Kind string
	// Weight is the resource weight this item consumes against
	// AdmissionConfig.MaxInflight. Zero or negative is treated as 1.
	Weight int
	// CompileLock is true when this item must respect the compile-class
	// concurrency ceiling (AdmissionConfig.CompileClassCap) scoped to
	// AdmissionConfig.RepoPath.
	CompileLock bool
	// Priority orders queued items (higher first) on the 0-100 scale
	// R-21.2 defines. Zero means PriorityDefault; within one priority
	// value the earliest-enqueued item is dequeued first.
	Priority int
}

// AdmissionConfig configures an AdmissionController. It is populated from
// the daemon's [governor].admission config section (08-INIT-CONFIG-SPEC.md
// §3, R-14.42). Every field has a safe default so an absent config block
// is not an error.
type AdmissionConfig struct {
	// QueueCap is the bounded priority queue's capacity. Zero or negative
	// defaults to DefaultQueueCap.
	QueueCap int
	// MaxInflight is the maximum number of admitted, unreleased weight
	// units at once. Zero or negative defaults to DefaultMaxInflight.
	MaxInflight int
	// CompileClassCap is the per-repo compile-class concurrency ceiling.
	// Zero or negative defaults to DefaultCompileClassCap.
	CompileClassCap int
	// SwapThreshold is the fraction of swap in use (0-1) above which new
	// admission is queued rather than granted immediately. Zero or
	// negative defaults to DefaultSwapThreshold.
	SwapThreshold float64
	// RepoPath is the repository this controller instance's compile-lock
	// ceiling scopes to. Canonicalized via filepath.Clean before use.
	RepoPath string
}

// Default tunables for a zero-value AdmissionConfig field, per
// 08-INIT-CONFIG-SPEC.md §3's "unconfigured -> safe default" contract.
const (
	// DefaultQueueCap is AdmissionConfig.QueueCap's default.
	DefaultQueueCap = 64
	// DefaultMaxInflight is AdmissionConfig.MaxInflight's default.
	DefaultMaxInflight = 4
	// DefaultCompileClassCap is AdmissionConfig.CompileClassCap's default
	// (06-FORGE-SPEC.md §5 rule 10: one heavy compile per repo per
	// session).
	DefaultCompileClassCap = 1
	// DefaultSwapThreshold is AdmissionConfig.SwapThreshold's default.
	DefaultSwapThreshold = 0.50
)

// ThrottleStage is the throttle ladder's discrete pressure stage
// (normal < warn < critical < halt), defined here so the S-26.T2/S-26.T3
// seam has no import cycle: S-26.T3's ladder depends on this package's
// StageProvider signature, never the reverse.
type ThrottleStage int

// The four throttle stages, in increasing severity order.
const (
	// StageNormal applies no admission throttling.
	StageNormal ThrottleStage = iota
	// StageWarn applies no admission throttling yet, but signals rising
	// pressure to observability consumers.
	StageWarn
	// StageCritical halves the controller's effective MaxInflight
	// (integer division, floor 1).
	StageCritical
	// StageHalt makes Admit return ErrThrottled immediately, admitting
	// and queuing nothing.
	StageHalt
)

// StageProvider reports the current throttle stage. A nil StageProvider
// means stage StageNormal (S-26.T3 installs a real provider at wiring
// time; until then Admit is ungated by throttle stage).
type StageProvider func() ThrottleStage

// stageProviderBox lets a nil StageProvider be stored in an atomic.Value,
// which rejects a bare nil interface.
type stageProviderBox struct{ sp StageProvider }

// SetStageProvider installs the throttle-stage seam S-26.T3 wires at
// composition time. Safe with concurrent Admit; unset means StageNormal.
func (ac *AdmissionController) SetStageProvider(sp StageProvider) {
	ac.stageProviderVal.Store(stageProviderBox{sp: sp})
}

// stage reads the installed StageProvider without ac.mu, so it is safe
// from drainQueueLocked while ac.mu is already held.
func (ac *AdmissionController) stage() ThrottleStage {
	v := ac.stageProviderVal.Load()
	if v == nil {
		return StageNormal
	}
	box := v.(stageProviderBox)
	if box.sp == nil {
		return StageNormal
	}
	return box.sp()
}

// Inflight returns the current sum of admitted, unreleased weights.
func (ac *AdmissionController) Inflight() int {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.inflight
}

// QueueDepth returns the current number of queued waiters.
func (ac *AdmissionController) QueueDepth() int {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.queue.Len()
}

// EnforcedCeiling reports the admission ceiling CURRENTLY in force -
// never cfg.MaxInflight's static value alone. At StageHalt the honest
// ceiling is zero additional room (Admit refuses every new request), so
// EnforcedCeiling reports exactly the work already admitted, never a
// higher number implying room that does not exist; StageCritical reports
// its halved effective limit; StageNormal/StageWarn report cfg.MaxInflight
// unchanged. This is the binding-ceiling-honesty seam P1-E18-W4-S40-T2's
// HeadroomModel denominates against.
func (ac *AdmissionController) EnforcedCeiling() int {
	stage := ac.stage()
	if stage == StageHalt {
		return ac.Inflight()
	}
	return ac.effectiveMaxInflight(stage)
}

// Permit is the receipt Admit returns on a successful admission. Callers
// must call Release exactly once when the admitted work finishes; Release
// is idempotent and safe to call more than once, and a zero-value Permit's
// Release is a safe no-op.
type Permit struct {
	state *permitState
}

// permitState is Permit's shared, pointer-identity backing so a Permit
// value can be copied freely (per its Release's value receiver) while
// still releasing its resources exactly once.
type permitState struct {
	release func()
	once    sync.Once
	// admittedAt is this permit's grant instant, per the controller's
	// injected Clock (never a bare time.Now). It has no admission-logic
	// consumer yet; admission_test.go's determinism assertions read it
	// directly to prove Admit's timestamping never touches the real wall
	// clock.
	admittedAt time.Time
}

// Release runs this permit's release logic exactly once, decrementing the
// admission controller's in-flight accounting, releasing any held
// compile-class lock, and waking the next eligible queued waiter.
func (p Permit) Release() {
	if p.state == nil {
		return
	}
	p.state.once.Do(p.state.release)
}

// admissionWaiter is one blocked Admit call's queue entry. It implements
// container/heap's element role via admissionQueue below.
type admissionWaiter struct {
	req      AdmissionRequest
	seq      int64
	index    int
	resultCh chan admissionResult
}

// admissionResult is what a queued waiter's resultCh delivers: either a
// granted Permit or the reason admission was refused.
type admissionResult struct {
	permit Permit
	err    error
}

// admissionQueue is a container/heap priority queue ordered
// (Priority desc, enqueue-sequence asc) - R-21.215 strikes FIFO in favor
// of this ordering.
type admissionQueue []*admissionWaiter

func (q admissionQueue) Len() int { return len(q) }

func (q admissionQueue) Less(i, j int) bool {
	if q[i].req.Priority != q[j].req.Priority {
		return q[i].req.Priority > q[j].req.Priority
	}
	return q[i].seq < q[j].seq
}

func (q admissionQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

// Push implements heap.Interface. Callers use heap.Push, never this
// directly.
func (q *admissionQueue) Push(x any) {
	w := x.(*admissionWaiter)
	w.index = len(*q)
	*q = append(*q, w)
}

// Pop implements heap.Interface. Callers use heap.Pop, never this
// directly.
func (q *admissionQueue) Pop() any {
	old := *q
	n := len(old)
	w := old[n-1]
	old[n-1] = nil
	w.index = -1
	*q = old[:n-1]
	return w
}

var _ heap.Interface = (*admissionQueue)(nil)

// requestWeight normalizes AdmissionRequest.Weight: zero or negative
// means 1.
func requestWeight(req AdmissionRequest) int {
	if req.Weight <= 0 {
		return 1
	}
	return req.Weight
}

// requestPriority normalizes AdmissionRequest.Priority: zero means
// PriorityDefault.
func requestPriority(req AdmissionRequest) int {
	if req.Priority == 0 {
		return PriorityDefault
	}
	return req.Priority
}

// swapFraction reports snap's swap-used fraction, treating a zero-total
// snapshot (Windows tier-2, ErrUnsupportedPlatform) as 0 rather than NaN -
// a documented, conservative degrade distinct from AdmissionController's
// fail-closed path for a missing Sampler entirely.
func swapFraction(snap ResourceSnapshot) float64 {
	if snap.SwapTotalBytes == 0 {
		return 0
	}
	return float64(snap.SwapUsedBytes) / float64(snap.SwapTotalBytes)
}
