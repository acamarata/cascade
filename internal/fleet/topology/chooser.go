// Purpose: the R-21.29 api-project CHOOSER: the eligibility predicate and
//
//	the credential pick inside a domain Rank already chose (R-21.102).
//	Enumeration is candidates.go; 429/401/403/5xx are chooser_errors.go.
//
// Inputs: a topology snapshot (SetTopology), then ChooseRequest values.
// Outputs: eligible Candidates and a chosen CredentialID (Choose).
// Constraints: no second selection objective (R-21.120); deterministic
//
//	(R-21.132); fail closed on a nil PrivacyGate or CredentialGate, an
//	unknown domain or a credential without its own evidence row.
//
// SPORT: fleet/topology/chooser/ADD (P1-E40-W9-S78-T1, P1-TOP-02b).

package topology

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ChooseRequest is Candidates' input: the model a caller needs, its
// resource estimate per real dimension name (never a batch gauge -- see
// batch.go for the gauge path), and the R-21.27 data class the privacy
// gate classifies against.
type ChooseRequest struct {
	ModelID   string
	Estimates map[string]int64
	DataClass string
}

// PrivacyGate is the R-21.27 seam: whether a request carrying dataClass
// may be served from domain d. A nil gate makes every candidate
// ineligible; BillingPrivacyGate is the production gate.
type PrivacyGate func(d QuotaDomain, dataClass string) bool

// BillingPrivacyGate admits dataClass on d when the billing cap alone
// admits it: free and discover domains carry public only, a paid domain
// any tier. An invalid billing tier or an unparseable class refuses.
func BillingPrivacyGate(d QuotaDomain, dataClass string) bool {
	tier, err := provider.ParseSensitivityTier(dataClass)
	if err != nil || !d.BillingTier.Valid() {
		return false
	}
	if d.BillingTier == BillingTierPaid {
		return true
	}
	return !tier.MoreRestrictiveThan(provider.SensitivityPublic)
}

// CredentialGate returns credential cred's own data-class ceiling in
// domain d. Choose admits cred only when TierAdmits the request against
// it; an error makes cred ineligible. EvidenceCredentialGate is the
// production gate.
type CredentialGate func(ctx context.Context, cred Credential, d QuotaDomain) (Ceiling, error)

// EvidenceCredentialGate uses the shared EvidenceKey in billing_promotion.go. A
// credential reads internal only when its domain is paid and its OWN
// stored row (stored, found) equals current(cred); anything else reads
// public with reason billing-cap. A nil fn keeps public; an invalid
// billing tier or a read error refuses.
func EvidenceCredentialGate(stored func(context.Context, CredentialID) (EvidenceKey, bool, error), current func(context.Context, Credential) (EvidenceKey, error)) CredentialGate {
	return func(ctx context.Context, cred Credential, d QuotaDomain) (Ceiling, error) {
		if !d.BillingTier.Valid() {
			return Ceiling{}, cascade.Newf(cascade.KindInvalidInput, "topology: domain %q has invalid billing tier %q", d.ID, d.BillingTier)
		}
		c := Ceiling{Tier: provider.SensitivityPublic, Reason: CeilingBillingCap, BillingTier: d.BillingTier, LaneType: LaneKindExternalAPI, DomainID: d.ID}
		if d.BillingTier != BillingTierPaid || stored == nil || current == nil {
			return c, nil
		}
		row, found, err := stored(ctx, cred.ID)
		if err != nil {
			return Ceiling{}, cascade.Wrapf(cascade.KindUnavailable, err, "topology: billing evidence for credential %q", cred.ID)
		}
		if !found || row == (EvidenceKey{}) {
			return c, nil
		}
		cur, err := current(ctx, cred)
		if err != nil {
			return Ceiling{}, cascade.Wrapf(cascade.KindUnavailable, err, "topology: current evidence key for credential %q", cred.ID)
		}
		if cur == row {
			c.Tier, c.Reason = provider.SensitivityInternal, CeilingRestrictedPermit
		}
		return c, nil
	}
}

// credOverride is the Chooser's in-memory credential health on top of the
// last snapshot, so a 401 from OnProviderError applies at once.
type credOverride struct {
	quarantined bool
	reason      string
	until       time.Time
}

// Chooser is the long-lived selection engine over the last SetTopology
// snapshot plus transient error state. Construct with NewChooser.
type Chooser struct {
	clock       Clock
	rng         *rand.Rand
	privacyGate PrivacyGate
	credGate    CredentialGate
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
// global source). credGate is required: a nil gate leaves every
// credential ineligible, as a nil privacyGate leaves every candidate
// ineligible and a nil cascadeFn makes OnScopeTransition refuse.
func NewChooser(clock Clock, seed int64, privacyGate PrivacyGate, credGate CredentialGate, cascadeFn CascadeFn) *Chooser {
	return &Chooser{
		clock:                 clock,
		rng:                   rand.New(rand.NewSource(seed)), //nolint:gosec // deterministic selection tie-break, not a security draw (R-21.132).
		privacyGate:           privacyGate,
		credGate:              credGate,
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

// SetTopology replaces the snapshot. Transient state keyed by ID
// (quarantines, scope constraints, ewma_429) survives a refresh.
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

// isDomainQuarantined reports the snapshot flag OR a transient 403 override.
func (c *Chooser) isDomainQuarantined(d QuotaDomain) bool {
	return d.Quarantined || c.domainQuarantined[d.ID]
}

// isScopeConstrained reports whether a 429 rate-limited scope (R-21.95).
func (c *Chooser) isScopeConstrained(scope LimitScopeID, now time.Time) bool {
	until, ok := c.scopeConstrainedUntil[scope]
	return ok && now.Before(until)
}

// credentialHealthy reports snapshot Health unless an override quarantines.
func (c *Chooser) credentialHealthy(cred Credential, now time.Time) bool {
	if ov, ok := c.credOverrides[cred.ID]; ok {
		if ov.quarantined && (ov.until.IsZero() || now.Before(ov.until)) {
			return false
		}
	}
	return cred.Health == CredentialOK
}

// eligible evaluates the five R-21.29 predicates in order and returns
// (false, reason) naming the first one that fails.
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
// lane: round-robin among the health==ok credentials of lane's quota
// domain whose own CredentialGate ceiling admits dataClass. It makes no
// cross-domain decision and never falls back to "the first one": a nil
// gate, an unknown domain, a bad class or an empty set refuses, and a
// gate read error is returned when it leaves nothing eligible.
func (c *Chooser) Choose(ctx context.Context, lane Lane, dataClass string) (CredentialID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := lane.QuotaDomainRef
	if c.credGate == nil {
		return "", cascade.New(cascade.KindUnavailable, "topology: no credential gate configured (fail closed)")
	}
	req, err := provider.ParseSensitivityTier(dataClass)
	if err != nil {
		return "", err
	}
	domain, found := c.domains[ref]
	if !found {
		return "", newNotFoundErr("quota domain", string(ref))
	}
	now := c.clock.Now()
	var ok []Credential
	for _, cr := range c.creds {
		if cr.QuotaDomainRef == ref && c.credentialHealthy(cr, now) {
			ok = append(ok, cr)
		}
	}
	sortCredentialsByID(ok)
	var admitted []Credential
	var readErr error
	for _, cr := range ok {
		ceil, gerr := c.credGate(ctx, cr, domain)
		if gerr != nil {
			if readErr == nil {
				readErr = gerr
			}
			continue
		}
		if TierAdmits(req, ceil) {
			admitted = append(admitted, cr)
		}
	}
	if len(admitted) == 0 {
		if readErr != nil {
			return "", readErr
		}
		return "", newNotFoundErr("credential", "no eligible credential in domain "+string(ref))
	}
	idx := c.rrCursor[ref] % len(admitted)
	c.rrCursor[ref] = idx + 1
	return admitted[idx].ID, nil
}

// sortCredentialsByID sorts cs ascending by ID so round robin is
// deterministic (never map order).
func sortCredentialsByID(cs []Credential) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0 && cs[j].ID < cs[j-1].ID; j-- {
			cs[j], cs[j-1] = cs[j-1], cs[j]
		}
	}
}
