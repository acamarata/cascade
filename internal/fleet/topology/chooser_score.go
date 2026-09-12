// Purpose: R-21.120's SignalBundle assembly -- the former dispatch_score
//
//	inputs, now an ATTRIBUTE bundle economics.Rank folds into its scarcity
//	term as a sixth utility component, never a second objective computed
//	here. dispatch_score itself is DELETED: this file computes no argmin
//	and no per-domain ranking.
//
// Inputs: a QuotaDomain and the instant now. Outputs: a SignalBundle.
// Constraints: the jitter draw comes from the Chooser's own seeded
//
//	rand.Source field (never a global source, R-21.132); the clock is
//	injected.
//
// SPORT: fleet/topology/chooser_score/ADD (P1-E40-W9-S78-T1).

package topology

import "time"

// latencyNormalizationWindow is the R-21.120 normalization constant:
// normalized_latency = p50 / 10s, clamped to [0,1].
const latencyNormalizationWindow = 10 * time.Second

// ewma429Alpha is R-21.120's smoothing constant for the scope-level
// exponential moving average of 429 occurrences.
const ewma429Alpha = 0.2

// SignalBundle is the attribute bundle Signals assembles for economics
// Rank's scarcity term (R-21.120's sixth utility component:
// `- rw * (2.0*ewma_429(scope) + 0.20*normalized_latency)`). Nothing in
// this package minimises or maximises across SignalBundle values -- Rank
// alone does.
type SignalBundle struct {
	Pressure          float64
	Ewma429           float64
	NormalizedLatency float64
	Jitter            float64
}

// recordEwma429 folds one 429 (sample=1) or non-429 (sample=0) observation
// into scope's running ewma_429, alpha=0.2 (R-21.120). Called by
// chooser_errors.go's OnProviderError; a scope with no observations yet
// starts at 0 ("no 429 history").
func (c *Chooser) recordEwma429(scope LimitScopeID, sample float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.ewma429[scope]
	c.ewma429[scope] = ewma429Alpha*sample + (1-ewma429Alpha)*prev
}

// ewma429For returns scope's current ewma_429, 0 for a scope with no
// recorded observations.
func (c *Chooser) ewma429For(scope LimitScopeID) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ewma429[scope]
}

// normalizedLatency clamps p50/10s to [0,1] (R-21.120).
func normalizedLatency(p50 time.Duration) float64 {
	if p50 <= 0 {
		return 0
	}
	v := float64(p50) / float64(latencyNormalizationWindow)
	return clampPressure(v, 0, 1)
}

// Signals assembles domain d's SignalBundle at instant now: pressure (the
// R-21.31/R-21.128 scalar, MAX over d's real dimensions), ewma_429(scope),
// normalized_latency (0 until a caller has recorded an observed p50 --
// this ticket introduces no latency-observation pipeline of its own) and
// a jitter draw from the Chooser's seeded source. reserve is the caller-
// supplied barrier input economics resolves from account config
// (topology reads no account config itself, per chooser_pressure.go).
func (c *Chooser) Signals(account Account, d QuotaDomain, reserve float64, now time.Time) SignalBundle {
	scope := ResolveScope(account, d)
	ewma := c.ewma429For(scope)
	c.mu.Lock()
	jitter := c.rng.Float64()
	c.mu.Unlock()
	return SignalBundle{
		Pressure:          DomainPressure(d, reserve, ewma, now),
		Ewma429:           ewma,
		NormalizedLatency: normalizedLatency(0),
		Jitter:            jitter,
	}
}
