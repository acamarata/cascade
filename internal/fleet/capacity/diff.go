// Purpose (this file): Clone (a pure, no-I/O deep copy of a FleetSnapshot,
// used by the SSE path to snapshot before emission) and Diff (a pure
// comparison returning a non-nil Delta only when a provider/node slot's
// state or bucket value actually changed).
//
// Inputs: two FleetSnapshot values.
// Outputs: a deep-copied FleetSnapshot (Clone) or a *Delta (Diff).
// Constraints: no I/O, no Clock - both operate purely on their arguments.
//
// SPORT: fleet.capacity.snapshot (diff, ADD, per T-1 sport_updates).

package capacity

// Delta is the non-empty result of comparing two FleetSnapshots: only the
// providers/nodes whose slot actually changed.
type Delta struct {
	ChangedProviders map[string]ProviderSlot `json:"changed_providers,omitempty"`
	RemovedProviders []string                `json:"removed_providers,omitempty"`
	ChangedNodes     map[string]NodeSlot     `json:"changed_nodes,omitempty"`
	RemovedNodes     []string                `json:"removed_nodes,omitempty"`
}

// empty reports whether d carries no actual change.
func (d *Delta) empty() bool {
	return d == nil || (len(d.ChangedProviders) == 0 && len(d.RemovedProviders) == 0 &&
		len(d.ChangedNodes) == 0 && len(d.RemovedNodes) == 0)
}

// Clone returns a deep copy of snap: mutating the result never affects
// snap's own maps/slices.
func Clone(snap FleetSnapshot) FleetSnapshot {
	out := FleetSnapshot{
		GeneratedAt: snap.GeneratedAt,
		Seq:         snap.Seq,
		Providers:   make(map[string]ProviderSlot, len(snap.Providers)),
		Nodes:       make(map[string]NodeSlot, len(snap.Nodes)),
	}
	for name, slot := range snap.Providers {
		out.Providers[name] = cloneProviderSlot(slot)
	}
	for id, slot := range snap.Nodes {
		out.Nodes[id] = cloneNodeSlot(slot)
	}
	if snap.TaskCapabilities != nil {
		out.TaskCapabilities = append([]TaskCapabilityRow(nil), snap.TaskCapabilities...)
	}
	return out
}

func cloneProviderSlot(slot ProviderSlot) ProviderSlot {
	buckets := make(map[BucketKind]Bucket, len(slot.Buckets))
	for k, b := range slot.Buckets {
		buckets[k] = b
	}
	slot.Buckets = buckets
	return slot
}

func cloneNodeSlot(slot NodeSlot) NodeSlot {
	slot.Toolchains = append([]string(nil), slot.Toolchains...)
	return slot
}

// Diff compares prev to next and returns a non-nil *Delta only when at
// least one provider or node slot's state or bucket value differs, or a
// slot present in prev is absent in next. Two snapshots with identical
// slot content (even with different GeneratedAt/Seq, which are never
// compared) return nil - "no material change".
func Diff(prev, next FleetSnapshot) *Delta {
	d := &Delta{
		ChangedProviders: map[string]ProviderSlot{},
		ChangedNodes:     map[string]NodeSlot{},
	}
	for name, slot := range next.Providers {
		if old, ok := prev.Providers[name]; !ok || !providerSlotEqual(old, slot) {
			d.ChangedProviders[name] = slot
		}
	}
	for name := range prev.Providers {
		if _, ok := next.Providers[name]; !ok {
			d.RemovedProviders = append(d.RemovedProviders, name)
		}
	}
	for id, slot := range next.Nodes {
		if old, ok := prev.Nodes[id]; !ok || !nodeSlotEqual(old, slot) {
			d.ChangedNodes[id] = slot
		}
	}
	for id := range prev.Nodes {
		if _, ok := next.Nodes[id]; !ok {
			d.RemovedNodes = append(d.RemovedNodes, id)
		}
	}
	if d.empty() {
		return nil
	}
	if len(d.ChangedProviders) == 0 {
		d.ChangedProviders = nil
	}
	if len(d.ChangedNodes) == 0 {
		d.ChangedNodes = nil
	}
	return d
}

func providerSlotEqual(a, b ProviderSlot) bool {
	if a.State != b.State || a.ReauthRequired != b.ReauthRequired || !a.ResetEstimate.Equal(b.ResetEstimate) {
		return false
	}
	if len(a.Buckets) != len(b.Buckets) {
		return false
	}
	for k, av := range a.Buckets {
		bv, ok := b.Buckets[k]
		if !ok || av.State != bv.State || av.FiveHour != bv.FiveHour || av.SevenDay != bv.SevenDay {
			return false
		}
	}
	return true
}

func nodeSlotEqual(a, b NodeSlot) bool {
	if a.Presence != b.Presence || a.CPUCores != b.CPUCores || a.RAMMB != b.RAMMB || a.GPU != b.GPU || a.Load != b.Load || a.TrustTier != b.TrustTier {
		return false
	}
	if len(a.Toolchains) != len(b.Toolchains) {
		return false
	}
	for i := range a.Toolchains {
		if a.Toolchains[i] != b.Toolchains[i] {
			return false
		}
	}
	return true
}
