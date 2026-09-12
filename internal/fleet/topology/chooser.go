// Purpose: the R-21.29 api-project CHOOSER -- the eligibility predicate and
//
//	the credential-selection loop only (R-21.102: economics.Rank is the
//	sole lane-and-domain selector; this package enumerates candidates and,
//	once Rank has picked a lane, picks a healthy credential inside that
//	lane's own quota domain). See candidates.go for enumeration,
//	chooser_score.go for the signal bundle, chooser_pressure.go for the
//	pressure scalar, chooser_errors.go for the 429/401/403/5xx transitions
//	and scope_transition.go for the reservation-rollback cascade.
//
// Inputs: a topology snapshot (SetTopology), then ChooseRequest values.
// Outputs: eligible Candidate values (candidates.go) and a chosen
//
//	CredentialID (Choose).
//
// Constraints: never a second selection objective (R-21.120) -- Choose
//
//	only round-robins a health==ok credential inside an ALREADY-CHOSEN
//	domain; determinism per R-21.132 (seeded rand.Source field, ascending
//	lane_id tie-break); fail closed on an unconfigured PrivacyGate, an
//	unknown lane/domain, or an empty topology snapshot.
//
// SPORT: fleet/topology/chooser/ADD (P1-E40-W9-S78-T1).

package topology

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// Clock is reconcile.go's injected wall-clock seam (06 Sec.2 -- no bare
// time.Now in domain logic); reused here rather than a second declaration.

// ChooseRequest is Candidates' input: the model a caller needs, its
// resource estimate per real dimension name (never a batch gauge -- see
// batch.go for the gauge path), and the R-21.27 data class the privacy
// gate classifies against.
type ChooseRequest struct {
	ModelID   string
	Estimates map[string]int64
	DataClass string
}

// PrivacyGate is the injected AN/S-77.T4 seam (R-21.27): whether a request
// carrying dataClass may be served from domain d. THIS PACKAGE DOES NOT
// EXIST YET IN THE TREE at the time this ticket landed (verified: no
// DataClass/privacy symbol anywhere under internal/fleet/topology as of
// S-77.T1/S-77.T3) despite the contract's "both are consumed here and
// neither is re-implemented" wording -- see the journal's CONTRADICTION
// entry. Candidates fails closed (every candidate ineligible) when no
// gate is configured, so an un-wired chooser can never leak past privacy
// by omission.
type PrivacyGate func(d QuotaDomain, dataClass string) bool

// credOverride is the Chooser's own in-memory view of a credential's
// health, applied on top of whatever SetTopology last loaded -- so a 401
// handled by OnProviderError is honoured immediately, without requiring a
// synchronous store round trip before the next Choose call.
type credOverride struct {
	quarantined bool
	reason      string
	until       time.Time
}

// Chooser is the long-lived selection engine: SetTopology installs a fresh
// snapshot (normally after every Reconcile); OnProviderError and
// OnScopeTransition layer transient state on top between snapshots. The
// zero value is not usable; construct with NewChooser.
type Chooser struct {
	clock       Clock
	rng         *rand.Rand
	privacyGate PrivacyGate
	cascadeFn   CascadeFn

	mu sync.Mutex

	accounts map[AccountID]Account
	domains  map[DomainID]QuotaDomain
	lanes    []Lane
	creds    map[CredentialID]Credential // keyed, grouped by QuotaDomainRef at read time

	ewma429               map[LimitScopeID]float64
	scopeConstrainedUntil map[LimitScopeID]time.Time
	scopeState            map[LimitScopeID]ScopeState
	scopeCascade          map[LimitScopeID]Cascade
	domainQuarantined     map[DomainID]bool
	credOverrides         map[CredentialID]credOverride
	rrCursor              map[DomainID]int
	backoffAttempts       map[DomainID]int
}

// NewChooser returns a ready Chooser. seed fixes rng.rand's source
// (R-21.132: every golden and test seeds it to a known value, never a
// global source). privacyGate and cascadeFn may be nil at construction --
// Candidates and OnScopeTransition fail closed until each is configured,
// which is intentional: an unwired chooser must refuse rather than guess.
func NewChooser(clock Clock, seed int64, privacyGate PrivacyGate, cascadeFn CascadeFn) *Chooser {
	return &Chooser{
		clock:                 clock,
		rng:                   rand.New(rand.NewSource(seed)), //nolint:gosec // deterministic selection tie-break, not a security draw (R-21.132).
		privacyGate:           privacyGate,
		cascadeFn:             cascadeFn,
		accounts:              map[AccountID]Account{},
		domains:               map[DomainID]QuotaDomain{},
		creds:                 map[CredentialID]Credential{},
		ewma429:               map[LimitScopeID]float64{},
		scopeConstrainedUntil: map[LimitScopeID]time.Time{},
		scopeState:            map[LimitScopeID]ScopeState{},
		scopeCascade:          map[LimitScopeID]Cascade{},
		domainQuarantined:     map[DomainID]bool{},
		credOverrides:         map[CredentialID]credOverride{},
		rrCursor:              map[DomainID]int{},
		backoffAttempts:       map[DomainID]int{},
	}
}

// SetTopology installs a fresh topology snapshot, replacing whatever
// SetTopology last loaded. Transient state from OnProviderError /
// OnScopeTransition (quarantines, scope constraints, ewma_429) survives a
// refresh -- it is keyed by ID, not by the snapshot's own object identity.
func (c *Chooser) SetTopology(accounts []Account, domains []QuotaDomain, lanes []Lane, credentials []Credential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accounts = make(map[AccountID]Account, len(accounts))
	for _, a := range accounts {
		c.accounts[a.ID] = a
	}
	c.domains = make(map[DomainID]QuotaDomain, len(domains))
	for _, d := range domains {
		c.domains[d.ID] = d
	}
	c.lanes = append([]Lane(nil), lanes...)
	c.creds = make(map[CredentialID]Credential, len(credentials))
	for _, cr := range credentials {
		c.creds[cr.ID] = cr
	}
}

// isDomainQuarantined reports d's quarantine state: the snapshot's own
// field OR'd with any transient override OnProviderError installed (a 403
// quarantines the domain immediately, ahead of the next SetTopology).
func (c *Chooser) isDomainQuarantined(d QuotaDomain) bool {
	return d.Quarantined || c.domainQuarantined[d.ID]
}

// isScopeConstrained reports whether scope is presently rate-limited
// (R-21.95): a 429 marks the SCOPE, not one domain, so every domain
// sharing it is disabled until the recorded until-instant.
func (c *Chooser) isScopeConstrained(scope LimitScopeID, now time.Time) bool {
	until, ok := c.scopeConstrainedUntil[scope]
	return ok && now.Before(until)
}

// credentialHealthy reports whether cred is presently selectable: its
// snapshot Health field, unless a transient override says otherwise.
func (c *Chooser) credentialHealthy(cred Credential, now time.Time) bool {
	if ov, ok := c.credOverrides[cred.ID]; ok {
		if ov.quarantined && (ov.until.IsZero() || now.Before(ov.until)) {
			return false
		}
	}
	return cred.Health == CredentialOK
}

// eligible evaluates the five R-21.29 predicates, in order, for lane
// bound to domain owned by account, against req at instant now. It
// returns (true, "") on success or (false, reason) naming the first
// failed predicate, so candidates.go can attach a reason per R-21.29's
// own requirement ("fleet lanes explain" in S-78.T3 renders it).
func (c *Chooser) eligible(account Account, domain QuotaDomain, lane Lane, req ChooseRequest, now time.Time) (bool, string) {
	if req.ModelID != "" && lane.ModelID != req.ModelID {
		return false, "supports_model: lane model does not match request"
	}
	if c.privacyGate == nil {
		return false, "privacy_allows: no privacy gate configured (fail closed)"
	}
	if !c.privacyGate(domain, req.DataClass) {
		return false, "privacy_allows: data class refused for this domain's billing tier"
	}
	if c.isDomainQuarantined(domain) {
		return false, "quarantined: domain is quarantined"
	}
	scope := ResolveScope(account, domain)
	if c.isScopeConstrained(scope, now) {
		return false, "scope_constrained: limit scope is rate-limited"
	}
	for dim, estimate := range req.Estimates {
		bucket, ok := domain.Dimensions[dim]
		if !ok {
			return false, "can_reserve: domain has no bucket for requested dimension " + dim
		}
		if !Reservable(bucket, estimate) {
			return false, "can_reserve: bucket cannot satisfy the requested estimate"
		}
	}
	return true, ""
}

// Choose implements R-21.29's residual role once Rank has already picked
// lane: round-robin among lane's quota domain's health==ok credentials.
// It makes no cross-domain decision and never falls back to "the first
// one" -- an empty eligible set is ErrTopologyNotFound (fail closed).
func (c *Chooser) Choose(_ context.Context, lane Lane) (CredentialID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	var ok []Credential
	for _, cr := range c.creds {
		if cr.QuotaDomainRef != lane.QuotaDomainRef {
			continue
		}
		if c.credentialHealthy(cr, now) {
			ok = append(ok, cr)
		}
	}
	if len(ok) == 0 {
		return "", newNotFoundErr("credential", "no healthy credential in domain "+string(lane.QuotaDomainRef))
	}
	sortCredentialsByID(ok)
	idx := c.rrCursor[lane.QuotaDomainRef] % len(ok)
	c.rrCursor[lane.QuotaDomainRef] = idx + 1
	return ok[idx].ID, nil
}

// sortCredentialsByID sorts cs ascending by ID, in place, so round-robin
// order is deterministic across runs (never Go map iteration order).
func sortCredentialsByID(cs []Credential) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].ID < cs[j-1].ID; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}
