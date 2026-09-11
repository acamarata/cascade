// Purpose: the [conductor.quota] policy layer -- QuotaConfig (the TOML
//
//	section's typed, validated shape), and QuotaPolicy.NextLane, the
//	ordering function P1-E11-W3-S22-T2's Router calls over its own
//	already sensitivity-filtered candidate set (R-21.264). NextLane never
//	selects on its own initiative; it only orders a set someone else
//	produced.
//
// Inputs: NextLane takes a context and the caller's exclusion set;
//
//	ParseQuotaConfig takes the generic config tree runtime.Config.Extra
//	already carries for sections runtime/config.go does not own.
//
// Outputs: NextLane returns the next available LaneID in spill_order, or
//
//	ErrAllLanesExhausted (a cascade.KindQuotaExhausted error) when every
//	candidate is excluded or rate-limited.
//
// Constraints: no bare time.Now (every timestamp comes from the injected
//
//	Clock); QuotaPolicy holds no Store dependency at all, so it cannot
//	issue a Store.Put for any lane, personal-account or otherwise --
//	quota_test.go's spy-Store test is regression insurance for that
//	invariant, not a conditional check keyed on account_kind.
//
// SPORT: provider · J · S-21 · T-1 · quota/spill routing policy
//
//	(P1-E10-W3-S21-T1).

package conductor

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LaneID names one provider lane, matching the S-20 registry's
// LaneRecord.LaneName (internal/providers/registry). Declared locally
// rather than imported so this package's public API does not couple
// callers to the registry's storage-layer types.
type LaneID string

// Clock abstracts the wall clock (Art.7.3: no bare time.Now). Structurally
// identical to internal/runtime.Clock and internal/providers/registry.Clock,
// so any of their concrete types already satisfy it.
type Clock interface {
	Now() time.Time
}

// ErrAllLanesExhausted is returned by NextLane when every lane in the
// (already-filtered) candidate set is excluded or currently rate-limited.
// It is the typed exhaustion error the contract requires: callers fail
// closed on it rather than falling back to an arbitrary or unconfigured
// lane.
var ErrAllLanesExhausted = cascade.New(cascade.KindQuotaExhausted,
	"conductor: quota: every lane in the candidate set is excluded or rate-limited")

// ErrInvalidQuotaConfig wraps a rejected [conductor.quota] value. Kept as
// a sentinel (rather than only a *cascade.Error) so a caller can
// errors.Is against it independently of the message text.
var ErrInvalidQuotaConfig = cascade.New(cascade.KindInvalidInput, "conductor: invalid [conductor.quota] config")

// QuotaConfig is the validated, typed [conductor.quota] TOML section
// (08-INIT-CONFIG-SPEC §3 config paths owned by C/S-04.T1's Load).
type QuotaConfig struct {
	// SpillOrder is the ordered list of lane names the policy advances
	// through. An empty SpillOrder is the fail-closed single-lane
	// default -- it is never itself an error.
	SpillOrder []LaneID
	// CeilingOverrides optionally caps admissions for a named lane within
	// a window. A lane absent from this map has no override.
	CeilingOverrides map[LaneID]int64
	// PersonalTracking must always resolve to false: personal-account
	// lane usage is never written to a tracking table (R-14.34). The
	// field exists only so an operator who writes `personal_tracking =
	// true` gets a structured rejection rather than a silently-ignored
	// key.
	PersonalTracking bool
}

// defaultQuotaConfig is the fail-closed single-lane default: an empty
// spill order, no ceiling overrides, tracking off.
func defaultQuotaConfig() QuotaConfig {
	return QuotaConfig{CeilingOverrides: map[LaneID]int64{}}
}

// ParseQuotaConfig extracts and validates the [conductor.quota] section
// out of extra, the generic map runtime.Config.Extra carries for every
// section runtime/config.go does not itself own (config.go is at
// R-14.117's 300-line cap; see this ticket's journal for why the section
// is parsed here rather than as a new runtime/config_quota.go typed
// field). A missing section is not an error: it resolves to the
// fail-closed default and divergent reports true so the caller can emit
// the required divergence event. A present-but-malformed section (wrong
// type, negative ceiling, non-string spill_order entry, or
// personal_tracking=true) is a hard *cascade.Error wrapping
// ErrInvalidQuotaConfig.
func ParseQuotaConfig(extra map[string]interface{}) (cfg QuotaConfig, divergent bool, err error) {
	raw, ok := lookupQuotaTable(extra)
	if !ok {
		return defaultQuotaConfig(), true, nil
	}
	cfg = defaultQuotaConfig()
	if v, present := raw["spill_order"]; present {
		order, perr := parseSpillOrder(v)
		if perr != nil {
			return QuotaConfig{}, false, perr
		}
		cfg.SpillOrder = order
	}
	if v, present := raw["ceiling"]; present {
		ceilings, perr := parseCeilings(v)
		if perr != nil {
			return QuotaConfig{}, false, perr
		}
		cfg.CeilingOverrides = ceilings
	}
	if v, present := raw["personal_tracking"]; present {
		b, ok := v.(bool)
		if !ok {
			return QuotaConfig{}, false, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidQuotaConfig,
				"conductor.quota.personal_tracking must be a bool")
		}
		if b {
			return QuotaConfig{}, false, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidQuotaConfig,
				"conductor.quota.personal_tracking must be false: personal-lane usage is never tracked (R-14.34)")
		}
		cfg.PersonalTracking = false
	}
	return cfg, false, nil
}

// lookupQuotaTable finds extra["conductor"]["quota"] as a map, or reports
// absent.
func lookupQuotaTable(extra map[string]interface{}) (map[string]interface{}, bool) {
	if extra == nil {
		return nil, false
	}
	condRaw, ok := extra["conductor"]
	if !ok {
		return nil, false
	}
	cond, ok := condRaw.(map[string]interface{})
	if !ok {
		return nil, false
	}
	quotaRaw, ok := cond["quota"]
	if !ok {
		return nil, false
	}
	quota, ok := quotaRaw.(map[string]interface{})
	return quota, ok
}

// parseSpillOrder converts a decoded TOML array into an ordered []LaneID,
// rejecting any non-string element.
func parseSpillOrder(v interface{}) ([]LaneID, error) {
	items, ok := v.([]interface{})
	if !ok {
		return nil, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidQuotaConfig,
			"conductor.quota.spill_order must be an array of lane names")
	}
	out := make([]LaneID, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidQuotaConfig,
				"conductor.quota.spill_order entries must be non-empty lane-name strings")
		}
		out = append(out, LaneID(s))
	}
	return out, nil
}

// parseCeilings converts a decoded TOML table of lane -> ceiling into a
// map[LaneID]int64, rejecting a negative or non-numeric ceiling.
func parseCeilings(v interface{}) (map[LaneID]int64, error) {
	table, ok := v.(map[string]interface{})
	if !ok {
		return nil, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidQuotaConfig,
			"conductor.quota.ceiling must be a table of lane_name = integer")
	}
	out := make(map[LaneID]int64, len(table))
	for lane, raw := range table {
		n, ok := toInt64(raw)
		if !ok || n < 0 {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidQuotaConfig,
				"conductor.quota.ceiling[%q] must be a non-negative integer", lane)
		}
		out[LaneID(lane)] = n
	}
	return out, nil
}

// toInt64 accepts either decoded-TOML integer representation
// (pelletier/go-toml/v2 decodes bare integers as int64).
func toInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

// QuotaPolicy holds the runtime spill/quota state for one resolved
// [conductor.quota] configuration: the ordered spill list and each lane's
// rate-limit window. The zero value is not usable; construct with
// NewQuotaPolicy. QuotaPolicy holds no Store dependency (see the package
// doc comment) -- it orders lanes and emits bus events, never persists.
type QuotaPolicy struct {
	clock Clock
	mu    sync.Mutex
	order []LaneID
	// limitedUntil records, per lane, the clock time a 429/exhaustion
	// window clears. A lane absent from this map has no active window.
	limitedUntil map[LaneID]time.Time
	// window is how long a single rate-limit demotion lasts before the
	// lane is eligible again. Fixed rather than config-driven: the
	// contract's [conductor.quota] schema has no per-lane window field,
	// only spill_order/ceiling/personal_tracking.
	window time.Duration
}

// defaultRateLimitWindow is how long NextLane treats a demoted lane as
// unavailable after Advance marks it. 60s matches the conductor's other
// short-horizon retry windows (no [conductor.quota] key configures this;
// see quota.go's QuotaPolicy.window doc comment).
const defaultRateLimitWindow = 60 * time.Second

// NewQuotaPolicy returns a QuotaPolicy over cfg's spill order, stamping
// every window from clk (never a bare time.Now).
func NewQuotaPolicy(cfg QuotaConfig, clk Clock) *QuotaPolicy {
	order := make([]LaneID, len(cfg.SpillOrder))
	copy(order, cfg.SpillOrder)
	return &QuotaPolicy{
		clock:        clk,
		order:        order,
		limitedUntil: make(map[LaneID]time.Time),
		window:       defaultRateLimitWindow,
	}
}

// NextLane returns the first lane in spill_order that is neither in
// excluded nor currently within an active rate-limit window, or
// ErrAllLanesExhausted if none qualifies. NextLane is an ORDERING
// function over a candidate set the caller has already produced
// (R-21.264): it does not itself evaluate capability, sensitivity, health
// or cost -- that filtering is P1-E11-W3-S22-T2's Router, the sole
// permitted caller (quota_arch_test.go asserts no other call site
// exists).
func (p *QuotaPolicy) NextLane(ctx context.Context, excluded []LaneID) (LaneID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.clock.Now()
	excludeSet := make(map[LaneID]bool, len(excluded))
	for _, id := range excluded {
		excludeSet[id] = true
	}
	for _, lane := range p.order {
		if excludeSet[lane] {
			continue
		}
		if until, limited := p.limitedUntil[lane]; limited && now.Before(until) {
			continue
		}
		return lane, nil
	}
	return "", ErrAllLanesExhausted
}
