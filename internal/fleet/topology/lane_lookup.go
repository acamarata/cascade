// Purpose: DomainForCredential, the read that maps a registry lane to the
// quota domain a reservation charges. Reconcile names each credential row
// after its registry lane (reconcileLane's CredentialID(l.LaneName)), so a
// registry lane resolves through its credential, never through the derived
// topology lane id: two unfiltered lanes of one provider share one topology
// lane but keep two credentials.
//
// Inputs: a CredentialID. Outputs: the credential's QuotaDomainRef.
// Constraints: read-only. An absent credential is KindNotFound; a
// quarantined credential, a quarantined domain or a domain with no
// non-retired lane is KindUnavailable wrapping ErrNoLiveQuotaDomain, so a
// caller tells those refusals from a failed read. Unknown capacity is never
// unlimited.
//
// SPORT: fleet/topology/lane_lookup/ADD (P1-CAP-05).

package topology

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrNoLiveQuotaDomain reports that a credential resolves to no quota domain
// a new reservation may charge (KindUnavailable).
var ErrNoLiveQuotaDomain = cascade.New(cascade.KindUnavailable, "topology: no live quota domain")

// DomainForCredential returns the quota domain credential id reserves
// against. It refuses an absent credential (KindNotFound), a quarantined
// credential or domain, and a domain whose every lane is retired
// (KindUnavailable).
func (s *Store) DomainForCredential(ctx context.Context, id CredentialID) (DomainID, error) {
	cred, err := s.GetCredential(ctx, id)
	if err != nil {
		return "", err
	}
	if cred.Health == CredentialQuarantined {
		return "", cascade.Wrapf(cascade.KindUnavailable, ErrNoLiveQuotaDomain, "topology: credential %q is quarantined", id)
	}
	domain, err := s.GetQuotaDomain(ctx, cred.QuotaDomainRef)
	if err != nil {
		return "", err
	}
	if domain.Quarantined {
		return "", cascade.Wrapf(cascade.KindUnavailable, ErrNoLiveQuotaDomain, "topology: quota domain %q is quarantined", domain.ID)
	}
	lanes, err := s.ListLanes(ctx, false)
	if err != nil {
		return "", err
	}
	for _, l := range lanes {
		if l.QuotaDomainRef == domain.ID {
			return domain.ID, nil
		}
	}
	return "", cascade.Wrapf(cascade.KindUnavailable, ErrNoLiveQuotaDomain, "topology: quota domain %q has no live lane", domain.ID)
}
