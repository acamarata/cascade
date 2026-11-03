// Purpose: the one seam the compositor reads gateway quota windows through
// (P1-GWY-04, contract:gateway-quota-bucket). The provider usage store owns
// the observations; this file only maps them: every window into the slot's
// additive quota list, and the 5h and week windows into the five_hour and
// seven_day utilization of the provider's buckets.
//
// Constraints: a stale or failed observation never fills a utilization
// (it stays WindowUtilizationUnknown, so the widget shows null); its last
// values stay in the quota list with Stale true. No path yields 0 for an
// unknown window.
// SPORT: fleet.capacity quota windows (ADD, P1-GWY-04).

package capacity

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/usage"
)

// QuotaSource reads one provider's quota windows from the usage store
// (usage.Reader.QuotaWindows in production).
type QuotaSource func(ctx context.Context, provider string) ([]usage.QuotaObservation, error)

// QuotaWindow is one quota window as fleet.capacity reports it.
type QuotaWindow struct {
	Window         topology.BucketWindow `json:"window"`
	Used           int64                 `json:"used"`
	Limit          int64                 `json:"limit"`
	UtilizationPct float64               `json:"utilization_pct"`
	ResetsAt       time.Time             `json:"resets_at"`
	ObservedAt     time.Time             `json:"observed_at"`
	Stale          bool                  `json:"stale"`
}

// SetQuotaSource installs q as this compositor's quota source; nil falls
// back to the process default (SetDefaultQuotaSource). Safe to call while
// UpdateProviders runs.
func (c *Compositor) SetQuotaSource(q QuotaSource) {
	c.mu.Lock()
	c.quota = q
	c.mu.Unlock()
}

// defaultQuota is the daemon's one quota source, read by every compositor
// without its own (the status.widget compositor is built in internal/daemon,
// before the gateway-quota registration runs).
var defaultQuota atomic.Pointer[QuotaSource]

// SetDefaultQuotaSource installs q as the process default quota source and
// returns a func restoring the previous one. The composition root calls it
// once per daemon; nil clears it.
func SetDefaultQuotaSource(q QuotaSource) (restore func()) {
	var next *QuotaSource
	if q != nil {
		next = &q
	}
	prev := defaultQuota.Swap(next)
	return func() { defaultQuota.CompareAndSwap(next, prev) }
}

// quotaSource returns the compositor's source, else the process default.
func (c *Compositor) quotaSource() QuotaSource {
	c.mu.RLock()
	q := c.quota
	c.mu.RUnlock()
	if q == nil {
		if p := defaultQuota.Load(); p != nil {
			q = *p
		}
	}
	return q
}

// withQuota adds slot's quota windows. A failed read keeps the previous
// slot's windows, all marked stale, and fills no utilization.
func (c *Compositor) withQuota(ctx context.Context, slot ProviderSlot) ProviderSlot {
	q := c.quotaSource()
	if q == nil {
		return slot
	}
	obs, err := q(ctx, slot.ProfileRef)
	if err != nil {
		c.mu.RLock()
		prev := c.providers[slot.ProfileRef].Quota
		c.mu.RUnlock()
		slot.Quota = staleQuota(prev)
		return slot
	}
	return applyQuota(slot, obs, c.clock.Now())
}

// applyQuota maps obs onto slot (see the file comment).
func applyQuota(slot ProviderSlot, obs []usage.QuotaObservation, now time.Time) ProviderSlot {
	if len(obs) == 0 {
		return slot
	}
	slot.Quota = make([]QuotaWindow, 0, len(obs))
	buckets := make(map[BucketKind]Bucket, len(slot.Buckets))
	for kind, b := range slot.Buckets {
		buckets[kind] = b
	}
	for _, o := range obs {
		util := o.Utilization()
		slot.Quota = append(slot.Quota, QuotaWindow{Window: o.Window, Used: o.Used, Limit: o.Limit,
			UtilizationPct: util, ResetsAt: o.ResetsAt, ObservedAt: o.ObservedAt, Stale: o.Stale})
		if o.Stale || util < 0 {
			continue
		}
		for kind, b := range buckets {
			if b.State == StateUnknown {
				continue
			}
			switch o.Window {
			case topology.BucketWindow5h:
				b.FiveHour.UtilizationPct = util
				if left := o.ResetsAt.Sub(now); left > b.FiveHour.ResetsIn {
					b.FiveHour.ResetsIn = left
				}
			case topology.BucketWindowWeek:
				b.SevenDay.UtilizationPct = util
			case topology.BucketWindowDay, topology.BucketWindowMonth, topology.BucketWindowRolling:
				// quota[] only: no widget bucket carries these windows.
			}
			buckets[kind] = b
		}
	}
	slot.Buckets = buckets
	return slot
}

// staleQuota returns a copy of q with every window marked stale.
func staleQuota(q []QuotaWindow) []QuotaWindow {
	if len(q) == 0 {
		return q
	}
	out := make([]QuotaWindow, len(q))
	for i, w := range q {
		w.Stale = true
		out[i] = w
	}
	return out
}
