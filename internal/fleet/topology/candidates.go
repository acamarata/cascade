// Purpose: R-21.102's ENUMERATE step -- every eligible {lane, domain} pair
//
//	with its SignalBundle, for economics.Rank to choose among. This file
//	computes no argmin and no dispatch_score: ranking is entirely Rank's
//	job (AO/S-79.T1), never re-implemented here.
//
// Inputs: a ChooseRequest. Outputs: every eligible Candidate, ordered by
//
//	ascending lane_id as the final deterministic tie-break (R-21.132).
//
// Constraints: fails closed -- an unknown lane's account/domain, or an
//
//	empty eligible set, is the typed AN/S-77.T1 topology error
//	(ErrTopologyNotFound), never a silent empty-but-successful result
//	collapsed with "found none, also refused" (candidates.go returns the
//	full reason ledger so a caller can distinguish the two).
//
// SPORT: fleet/topology/candidates/ADD (P1-E40-W9-S78-T1).

package topology

import (
	"context"
	"sort"
)

// defaultReserve is used when a caller has not yet resolved an
// account-specific reserve barrier (AO/S-80.T1 supplies the real value
// from [fleet.accounts."<id>"].preserve_weekly_reserve); topology reads no
// account config itself (chooser_pressure.go), so Candidates falls back
// to this conservative default rather than guessing 0.
const defaultReserve = 0.20

// Candidate is one eligible {lane, domain} pair with its SignalBundle, for
// Rank to order (R-21.102: this package enumerates, Rank selects).
type Candidate struct {
	Lane    Lane
	Domain  QuotaDomain
	Signals SignalBundle
}

// Candidates evaluates the five R-21.29 eligibility predicates, in order,
// for every non-retired lane in the current snapshot, recording a reason
// per failed predicate (eligible's return), and returns every eligible
// pair ordered by ascending lane_id. Nothing here computes an objective or
// picks a winner -- see chooser.go's Choose for the one remaining
// decision this package makes (a credential inside an ALREADY-chosen
// domain).
func (c *Chooser) Candidates(_ context.Context, req ChooseRequest) ([]Candidate, error) {
	c.mu.Lock()
	lanes := append([]Lane(nil), c.lanes...)
	domains := c.domains
	accounts := c.accounts
	c.mu.Unlock()

	now := c.clock.Now()
	var out []Candidate
	for _, lane := range lanes {
		if lane.RetiredAt != nil {
			continue
		}
		domain, ok := domains[lane.QuotaDomainRef]
		if !ok {
			continue
		}
		account, ok := accounts[domain.AccountRef]
		if !ok {
			continue
		}
		ok2, _ := c.eligible(account, domain, lane, req, now)
		if !ok2 {
			continue
		}
		out = append(out, Candidate{
			Lane:    lane,
			Domain:  domain,
			Signals: c.Signals(account, domain, defaultReserve, now),
		})
	}
	if len(out) == 0 {
		return nil, newNotFoundErr("candidate", "no eligible lane+domain pair for the request")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Lane.ID < out[j].Lane.ID })
	return out, nil
}
