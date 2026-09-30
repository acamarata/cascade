// Purpose: ceiling enforcement and rolling-window admission tracking for QuotaPolicy (AUD-030).
// Inputs: LaneID and QuotaConfig.
// Outputs: AdmitLane and Apply methods on QuotaPolicy, ErrLaneCeilingReached sentinel.
// Constraints: injected Clock, no sleep, file <= 300 lines, functions <= 50 lines.
// SPORT: conductor.quota/AUD-030 (P1-CAP-14).

package conductor

import (
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CeilingWindow is the rolling admission cap window (AUD-030). Fixed at
// 1 hour per the contract decision.
const CeilingWindow = time.Hour

// ErrLaneCeilingReached is returned by AdmitLane when a lane has reached
// its configured ceiling for the current CeilingWindow.
var ErrLaneCeilingReached = cascade.New(cascade.KindQuotaExhausted,
	"conductor: quota: lane ceiling reached for this window")

// newQuotaPolicyWithCeilings constructs a QuotaPolicy initialized with
// cfg's spill order and ceiling overrides.
func newQuotaPolicyWithCeilings(cfg QuotaConfig, clk Clock) *QuotaPolicy {
	order := make([]LaneID, len(cfg.SpillOrder))
	copy(order, cfg.SpillOrder)
	ceilings := make(map[LaneID]int64, len(cfg.CeilingOverrides))
	for k, v := range cfg.CeilingOverrides {
		ceilings[k] = v
	}
	return &QuotaPolicy{
		clock:        clk,
		order:        order,
		ceilings:     ceilings,
		admissions:   make(map[LaneID][]time.Time),
		limitedUntil: make(map[LaneID]time.Time),
		window:       defaultRateLimitWindow,
	}
}

// AdmitLane checks whether lane can be admitted under its configured
// ceiling for the rolling CeilingWindow. If the ceiling has been reached,
// it returns ErrLaneCeilingReached; otherwise it records the admission
// under the mutex and returns nil.
func (p *QuotaPolicy) AdmitLane(lane LaneID) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.clock.Now()
	if !p.canAdmitLocked(lane, now) {
		return ErrLaneCeilingReached
	}
	p.recordAdmissionLocked(lane, now)
	return nil
}

// Apply reloads cfg's spill order and ceilings under the mutex while
// preserving existing admission counters and rate-limit windows.
func (p *QuotaPolicy) Apply(cfg QuotaConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	order := make([]LaneID, len(cfg.SpillOrder))
	copy(order, cfg.SpillOrder)
	p.order = order

	ceilings := make(map[LaneID]int64, len(cfg.CeilingOverrides))
	for k, v := range cfg.CeilingOverrides {
		ceilings[k] = v
	}
	p.ceilings = ceilings
}

// canAdmitLocked prunes entries older than CeilingWindow and reports whether
// lane may be admitted. Must be called under p.mu.
func (p *QuotaPolicy) canAdmitLocked(lane LaneID, now time.Time) bool {
	p.pruneAdmissionsLocked(lane, now)
	ceiling, hasCeiling := p.ceilings[lane]
	if !hasCeiling {
		return true
	}
	return int64(len(p.admissions[lane])) < ceiling
}

// recordAdmissionLocked records an admission at now. Must be called under p.mu.
func (p *QuotaPolicy) recordAdmissionLocked(lane LaneID, now time.Time) {
	p.admissions[lane] = append(p.admissions[lane], now)
}

// pruneAdmissionsLocked removes admissions older than CeilingWindow. Must be
// called under p.mu.
func (p *QuotaPolicy) pruneAdmissionsLocked(lane LaneID, now time.Time) {
	cutoff := now.Add(-CeilingWindow)
	entries := p.admissions[lane]
	n := 0
	for _, t := range entries {
		if t.After(cutoff) {
			entries[n] = t
			n++
		}
	}
	p.admissions[lane] = entries[:n]
}

// exhaustedWithCeilings returns ErrAllLanesExhausted wrapped with the names
// of the lanes skipped for reaching their ceiling.
func exhaustedWithCeilings(skipped []LaneID) error {
	names := make([]string, len(skipped))
	for i, l := range skipped {
		names[i] = string(l)
	}
	return cascade.Wrapf(cascade.KindQuotaExhausted, ErrAllLanesExhausted,
		"conductor: quota: every lane in the candidate set is excluded, rate-limited, or ceiling reached (ceiling reached for: %s)",
		strings.Join(names, ", "))
}
