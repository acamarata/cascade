// Purpose: contract:inbox-provenance - the immutable Origin a notification
//
//	carries and the one stamp that derives its DataClass and Trust from
//	that Origin, never from a producer-set class or trust value.
//
// Inputs: a Notification on either ingestion path (Notifier.prepare and
//
//	NotificationRouter.handle) and the Inbox's re-record of a known id.
//
// Outputs: a stamped Notification; RaiseDataClass and JoinItems for the
//
//	raise-only and join-only updates that may follow.
//
// Constraints: unknown is never permission. A zero Origin, an empty
//
//	Source, an invalid tier, an invalid provenance or an unknown
//	Visibility resolves to SensitivityLocalOnly and untrusted-source.
//	Trust is trusted only for a closed producer list. No path lowers a
//	stored class or clears an untrusted trust.
//
// SPORT: internal.notify.provenance/ADDED (P1-HRN-03).

package notify

import (
	"slices"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Origin names where a Notification came from: the producer, the record it
// reports on, the sensitivity tier of that record and the producer's trust
// claim. The Inbox keeps the first Origin it sees for an id.
type Origin struct {
	// Source names the producer (for example "supervision.stall").
	Source string
	// RecordRef points at the record the notification reports on.
	RecordRef string
	// Tier is the sensitivity tier of the referenced record.
	Tier provider.SensitivityTier
	// Trust is the producer's provenance claim; it counts only for a
	// producer listed in TrustedProducers.
	Trust provider.Provenance
}

// TrustedProducers is the closed list of Origin sources whose trusted claim
// is honoured. Every other source stamps untrusted-source.
var TrustedProducers = []string{"notify.away.digest", "supervision.stall"}

// ErrDataClassLowered reports an attempt to set a less restrictive data
// class than the one a notification already carries.
var ErrDataClassLowered = cascade.New(cascade.KindPermissionDenied, "notify: data class cannot be lowered")

// visibilityFloor is the data class every known Visibility starts from.
const visibilityFloor = provider.SensitivityInternal

// stamp is the only writer of n.DataClass and n.Trust. It overwrites any
// producer-set value with the class and trust derived from n.Origin and
// n.Visibility.
func stamp(n *Notification) {
	n.DataClass, n.Trust = originClass(n.Origin, n.Visibility)
}

// originClass derives the data class and trust for o seen under v.
func originClass(o Origin, v Visibility) (provider.SensitivityTier, provider.Provenance) {
	trust, err := provider.ParseProvenance(string(o.Trust))
	if o == (Origin{}) || o.Source == "" || !o.Tier.Valid() || err != nil || !knownVisibilities[v] {
		return provider.SensitivityLocalOnly, provider.ProvenanceUntrustedSource
	}
	if !slices.Contains(TrustedProducers, o.Source) {
		trust = provider.ProvenanceUntrustedSource
	}
	return provider.JoinSensitivity(o.Tier, visibilityFloor), provider.JoinProvenance(trust)
}

// RaiseDataClass sets n.DataClass to to when to is at least as restrictive
// as the current class. A lower or invalid target is refused with
// ErrDataClassLowered and n is left unchanged.
func RaiseDataClass(n *Notification, to provider.SensitivityTier) error {
	if !to.Valid() {
		return ErrDataClassLowered
	}
	if n.DataClass.MoreRestrictiveThan(to) {
		return ErrDataClassLowered
	}
	n.DataClass = provider.JoinSensitivity(n.DataClass, to)
	return nil
}

// JoinItems returns the most restrictive data class and the least trusted
// provenance across items. No items joins to local-only and untrusted-source.
func JoinItems(items []Notification) (provider.SensitivityTier, provider.Provenance) {
	if len(items) == 0 {
		return provider.SensitivityLocalOnly, provider.ProvenanceUntrustedSource
	}
	tiers := make([]provider.SensitivityTier, 0, len(items))
	trusts := make([]provider.Provenance, 0, len(items))
	for _, it := range items {
		tiers = append(tiers, it.DataClass)
		trusts = append(trusts, it.Trust)
	}
	return provider.JoinSensitivity(tiers...), provider.JoinProvenance(trusts...)
}

// mergeRecord is Inbox.record's update of a known id: next replaces prev
// except that the first Origin is kept and class and trust only join.
func mergeRecord(prev, next Notification) Notification {
	next.Origin = prev.Origin
	next.DataClass, next.Trust = JoinItems([]Notification{prev, next})
	return next
}
