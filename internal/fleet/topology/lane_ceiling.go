// Purpose: the per-read data-class ceiling of one lane (P1-TOP-02a,
//
//	contract:lane-data-class-gate): the most sensitive SensitivityTier
//	the lane may carry, with the reason that capped it, and TierAdmits,
//	the one rank comparison a router uses against it.
//
// Inputs: the Store rows (credential, quota domain, lane, runtime profile),
//
//	the lane type, the restricted permit and two injected seams:
//	SensitivityTable (the conductor's one lane table; this package cannot
//	import the conductor, which imports it) and PaidEvidenceCurrent (the
//	stored-evidence-key check of contract:domain-billing-promotion).
//
// Outputs: a Ceiling or a *cascade.Error.
// Constraints: every read recomputes (no cache). An unset seam, an unknown
//
//	lane or row, an invalid billing tier, a DB error and an unresolved
//	lane each refuse with an error; none of them ever means "no cap".
//	Only a paid domain whose evidence is current lifts the public cap.
//
// SPORT: fleet/topology/lane_ceiling/ADD (P1-TOP-02a).

package topology

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CeilingReason names what capped a lane's Ceiling.
type CeilingReason string

// The five closed CeilingReason members.
const (
	CeilingBillingCap       CeilingReason = "billing-cap"
	CeilingLaneType         CeilingReason = "lane-type"
	CeilingRestrictedPermit CeilingReason = "restricted-permit"
	CeilingOfferingPolicy   CeilingReason = "offering-policy"
	CeilingLocalEndpoint    CeilingReason = "local-endpoint"
)

// Valid reports whether r is one of the five closed members.
func (r CeilingReason) Valid() bool {
	switch r {
	case CeilingBillingCap, CeilingLaneType, CeilingRestrictedPermit, CeilingOfferingPolicy, CeilingLocalEndpoint:
		return true
	}
	return false
}

// LaneKind is the wire name of the conductor's LaneType (its String form),
// so this package can name a lane type without importing the conductor.
type LaneKind string

// The three LaneKind names, equal to conductor.LaneType.String().
const (
	LaneKindControllerLocal LaneKind = "controller-local"
	LaneKindExternalAPI     LaneKind = "external-api"
	LaneKindUnresolved      LaneKind = "unresolved"
)

// Valid reports whether k is one of the three names.
func (k LaneKind) Valid() bool {
	return k == LaneKindControllerLocal || k == LaneKindExternalAPI || k == LaneKindUnresolved
}

// SensitivityTable is the seam to conductor.SensitivityAllows, the single
// tier x lane table. The conductor wires it; while it is nil every ceiling
// read refuses.
var SensitivityTable func(t provider.SensitivityTier, lane LaneKind, restrictedPermitted bool) bool

// PaidEvidenceCurrent reports whether domain d's stored billing evidence
// key equals the provider's current key. While it is nil a paid domain
// stays capped at public; an error refuses the read.
var PaidEvidenceCurrent func(ctx context.Context, s *Store, d QuotaDomain) (bool, error)

// Ceiling is one lane's data-class ceiling for one read.
type Ceiling struct {
	Tier        provider.SensitivityTier
	Reason      CeilingReason
	BillingTier BillingTier
	LaneType    LaneKind
	DomainID    DomainID
}

// tierOrder lists the tiers from most to least restrictive.
var tierOrder = []provider.SensitivityTier{
	provider.SensitivityLocalOnly, provider.SensitivityRestricted,
	provider.SensitivityInternal, provider.SensitivityPublic,
}

// TierAdmits reports whether a request at tier req may run under c. An
// invalid req reads as local-only; a zero or invalid Ceiling admits nothing.
func TierAdmits(req provider.SensitivityTier, c Ceiling) bool {
	if !c.Tier.Valid() || !c.Reason.Valid() {
		return false
	}
	if !req.Valid() {
		req = provider.SensitivityLocalOnly
	}
	return !req.MoreRestrictiveThan(c.Tier)
}

// PolicyKnown reports whether an offering policy declares both its
// training and retention terms ("" and "unknown" are not declarations).
func PolicyKnown(p ProviderPolicy) bool {
	return flagKnown(p, "training") && flagKnown(p, "retention")
}

func flagKnown(p ProviderPolicy, key string) bool {
	v := p.Flags[key]
	return v != "" && v != "unknown"
}

// policyPermitsPrivate reports whether an external offering may carry
// data above public: its terms are declared and it does not train.
func policyPermitsPrivate(p ProviderPolicy) bool {
	return PolicyKnown(p) && p.Flags["training"] == "false"
}

// computeCeiling is the pure ceiling rule over already-read rows.
func computeCeiling(lt LaneKind, permitted bool, d QuotaDomain, paidCurrent bool, pol ProviderPolicy) (Ceiling, error) {
	if !d.BillingTier.Valid() {
		return Ceiling{}, cascade.Newf(cascade.KindInvalidInput, "topology: domain %q has invalid billing tier %q", d.ID, d.BillingTier)
	}
	if !lt.Valid() || lt == LaneKindUnresolved {
		return Ceiling{}, cascade.Newf(cascade.KindPolicyDenied, "topology: lane type %q has no data-class ceiling", lt)
	}
	table := SensitivityTable
	if table == nil {
		return Ceiling{}, cascade.New(cascade.KindUnavailable, "topology: sensitivity table not wired")
	}
	c := Ceiling{BillingTier: d.BillingTier, LaneType: lt, DomainID: d.ID}
	found := false
	for _, t := range tierOrder {
		if table(t, lt, permitted) {
			c.Tier, found = t, true
			break
		}
	}
	if !found {
		return Ceiling{}, cascade.Newf(cascade.KindPolicyDenied, "topology: lane type %q admits no tier", lt)
	}
	switch {
	case c.Tier == provider.SensitivityLocalOnly:
		c.Reason = CeilingLocalEndpoint
	case c.Tier == provider.SensitivityInternal && !permitted:
		c.Reason = CeilingRestrictedPermit
	default:
		c.Reason = CeilingLaneType
	}
	if lt != LaneKindControllerLocal && !policyPermitsPrivate(pol) && c.Tier.MoreRestrictiveThan(provider.SensitivityPublic) {
		c.Tier, c.Reason = provider.SensitivityPublic, CeilingOfferingPolicy
	}
	if (d.BillingTier != BillingTierPaid || !paidCurrent) && c.Tier.MoreRestrictiveThan(provider.SensitivityPublic) {
		c.Tier, c.Reason = provider.SensitivityPublic, CeilingBillingCap
	}
	return c, nil
}

// paidCurrent asks the evidence seam about a paid domain; any other tier
// or an unset seam is not current.
func (s *Store) paidCurrent(ctx context.Context, d QuotaDomain) (bool, error) {
	fn := PaidEvidenceCurrent
	if d.BillingTier != BillingTierPaid || fn == nil {
		return false, nil
	}
	ok, err := fn(ctx, s, d)
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "topology: billing evidence for %q", d.ID)
	}
	return ok, nil
}

// ceilingFor reads the domain and evidence and computes the ceiling.
func (s *Store) ceilingFor(ctx context.Context, domain DomainID, lt LaneKind, permitted bool, pol ProviderPolicy) (Ceiling, error) {
	d, err := s.GetQuotaDomain(ctx, domain)
	if err != nil {
		return Ceiling{}, err
	}
	paid, err := s.paidCurrent(ctx, d)
	if err != nil {
		return Ceiling{}, err
	}
	return computeCeiling(lt, permitted, d, paid, pol)
}

// CeilingForRegistryLane returns the ceiling of the registry lane named
// laneName (its credential id, as Reconcile names it) under lane type lt.
func (s *Store) CeilingForRegistryLane(ctx context.Context, laneName string, lt LaneKind, permitted bool) (Ceiling, error) {
	cred, err := s.GetCredential(ctx, CredentialID(laneName))
	if err != nil {
		return Ceiling{}, err
	}
	lanes, err := s.ListLanes(ctx, false)
	if err != nil {
		return Ceiling{}, err
	}
	for _, l := range lanes {
		if l.CredentialRef != nil && *l.CredentialRef == cred.ID {
			return s.ceilingFor(ctx, cred.QuotaDomainRef, lt, permitted, l.OfferingSnapshot.ProviderPolicy)
		}
	}
	return Ceiling{}, newNotFoundErr("lane for credential", laneName)
}

// CeilingForLane returns the ceiling of topology lane id; its lane type
// comes from the runtime profile's endpoint (EndpointIsLocal).
func (s *Store) CeilingForLane(ctx context.Context, id LaneID, permitted bool) (Ceiling, error) {
	l, err := s.GetLane(ctx, id)
	if err != nil {
		return Ceiling{}, err
	}
	p, err := s.GetRuntimeProfile(ctx, l.RuntimeProfileRef)
	if err != nil {
		return Ceiling{}, err
	}
	lt := LaneKindExternalAPI
	if EndpointIsLocal(p) {
		lt = LaneKindControllerLocal
	}
	return s.ceilingFor(ctx, l.QuotaDomainRef, lt, permitted, l.OfferingSnapshot.ProviderPolicy)
}
