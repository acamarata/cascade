// Purpose: domain billing promotion with stored evidence. A quota domain
//
//	reaches billing_tier "paid" only through PromoteDomain, which writes
//	the evidence rows and the tier change in one transaction. Demotion
//	is always allowed; pruning drops evidence whose key no longer
//	matches and demotes a paid domain left without evidence.
//
// Inputs: the Store, caller-built BillingEvidence rows, an EvidenceKeyFn.
// Outputs: config_domain_billing_evidence rows and billing_tier changes.
// Constraints: storage only; no credential value in any row (fingerprints
//
//	and vault references only); every key is resolved before any delete.
//
// SPORT: fleet/topology/billing_promotion/ADD (P1-TOP-19).

package topology

import (
	"context"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// EvidenceOutcomePassed is the only TestOutcome a promotion accepts.
const EvidenceOutcomePassed = "passed"

// ErrBillingEvidenceRefused reports a paid promotion refused because its
// evidence is missing or does not prove a passing test.
var ErrBillingEvidenceRefused = cascade.New(cascade.KindInvalidInput, "topology: paid promotion requires passing test evidence")

// EvidenceKey identifies the provider endpoint and credential a piece of
// evidence was gathered against. CredentialFingerprint is a one-way
// fingerprint and AuthRef a vault key name; neither is a credential value.
type EvidenceKey struct {
	Provider              string
	InstanceID            string
	Endpoint              string
	AuthRef               string
	CredentialFingerprint string
}

// EvidenceKeyFn resolves an account's current EvidenceKey. A KindNotFound
// error means the account's key is gone; any other error aborts a prune.
type EvidenceKeyFn func(ctx context.Context, account AccountID) (EvidenceKey, error)

// BillingEvidence is one stored proof that a domain's paid tier was tested.
type BillingEvidence struct {
	DomainID      DomainID
	Key           EvidenceKey
	PromotedBy    string
	TestOutcome   string
	ProbeKind     string
	ProbeStatus   int
	ProbeEndpoint string
	ObservedAt    time.Time
}

// afterEvidenceInsert is a test seam run after the evidence inserts and
// before the tier write; production code never sets it.
var afterEvidenceInsert func() error

const evidenceColumns = `domain_id, provider, instance_id, endpoint, auth_ref, credential_fingerprint,
	promoted_by, test_outcome, probe_kind, probe_status, probe_endpoint, observed_at`

// PromoteDomain sets domain id to paid, writing ev in the same transaction.
// It refuses (ErrBillingEvidenceRefused) empty evidence, a row naming another
// domain, a non-passing outcome or a missing PromotedBy, and refuses an
// unknown domain with ErrTopologyNotFound. A refusal writes nothing.
func (s *Store) PromoteDomain(ctx context.Context, id DomainID, ev []BillingEvidence) error {
	if err := validateEvidence(id, ev); err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *Store) error {
		if _, err := tx.GetQuotaDomain(ctx, id); err != nil {
			return err
		}
		for _, e := range ev {
			if err := tx.insertEvidence(ctx, e); err != nil {
				return err
			}
		}
		if afterEvidenceInsert != nil {
			if err := afterEvidenceInsert(); err != nil {
				return err
			}
		}
		return tx.setBillingTier(ctx, id, BillingTierPaid)
	})
}

// DemoteDomain sets domain id back to discover. It needs no evidence and
// keeps the stored evidence rows as history.
func (s *Store) DemoteDomain(ctx context.Context, id DomainID) error {
	return s.setBillingTier(ctx, id, BillingTierDiscover)
}

// BillingEvidenceFor returns domain id's evidence rows in insertion order.
func (s *Store) BillingEvidenceFor(ctx context.Context, id DomainID) ([]BillingEvidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+evidenceColumns+` FROM `+tableBillingEvidence+` WHERE domain_id = ? ORDER BY id`, string(id))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: query billing evidence")
	}
	defer func() { _ = rows.Close() }()
	out := make([]BillingEvidence, 0)
	for rows.Next() {
		var e BillingEvidence
		var observed int64
		if err := rows.Scan(&e.DomainID, &e.Key.Provider, &e.Key.InstanceID, &e.Key.Endpoint, &e.Key.AuthRef,
			&e.Key.CredentialFingerprint, &e.PromotedBy, &e.TestOutcome, &e.ProbeKind, &e.ProbeStatus,
			&e.ProbeEndpoint, &observed); err != nil {
			return nil, wrapScan(err, "billing_evidence")
		}
		e.ObservedAt = time.UnixMilli(observed).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: read billing evidence")
	}
	return out, nil
}

// ForgetProviderEvidence deletes every evidence row for provider, demotes
// any paid domain left without evidence, and returns the rows deleted.
func (s *Store) ForgetProviderEvidence(ctx context.Context, provider string) (int, error) {
	if strings.TrimSpace(provider) == "" {
		return 0, cascade.New(cascade.KindInvalidInput, "topology: ForgetProviderEvidence requires a provider")
	}
	stale, err := s.listEvidenceRefs(ctx, `WHERE e.provider = ?`, provider)
	if err != nil {
		return 0, err
	}
	return s.deleteEvidence(ctx, stale)
}

// PruneBillingEvidence resolves every referenced account's key through fn
// first, then in one transaction deletes rows whose account key is gone
// (KindNotFound) or changed, and demotes paid domains left without
// evidence. Any other fn error deletes nothing and returns (0, err).
func (s *Store) PruneBillingEvidence(ctx context.Context, fn EvidenceKeyFn) (int, error) {
	if fn == nil {
		return 0, cascade.New(cascade.KindInvalidInput, "topology: PruneBillingEvidence requires a key function")
	}
	refs, err := s.listEvidenceRefs(ctx, ``)
	if err != nil {
		return 0, err
	}
	type resolved struct {
		key  EvidenceKey
		gone bool
	}
	keys := make(map[AccountID]resolved)
	for _, r := range refs {
		if _, ok := keys[r.account]; ok {
			continue
		}
		key, kerr := fn(ctx, r.account)
		switch {
		case kerr == nil:
			keys[r.account] = resolved{key: key}
		case cascade.HasKind(kerr, cascade.KindNotFound):
			keys[r.account] = resolved{gone: true}
		default:
			kind, ok := cascade.KindOf(kerr)
			if !ok {
				kind = cascade.KindUnavailable
			}
			return 0, cascade.Wrapf(kind, kerr, "topology: resolve evidence key for account %q", r.account)
		}
	}
	stale := make([]evidenceRef, 0)
	for _, r := range refs {
		if k := keys[r.account]; k.gone || k.key != r.key {
			stale = append(stale, r)
		}
	}
	return s.deleteEvidence(ctx, stale)
}

// evidenceRef is one stored row's identity, owning account and key.
type evidenceRef struct {
	id      int64
	domain  DomainID
	account AccountID
	key     EvidenceKey
}

// listEvidenceRefs reads evidence rows joined to their domain's account.
func (s *Store) listEvidenceRefs(ctx context.Context, where string, args ...any) ([]evidenceRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.domain_id, d.account_ref, e.provider, e.instance_id,
		e.endpoint, e.auth_ref, e.credential_fingerprint FROM `+tableBillingEvidence+` e
		JOIN `+tableQuotaDomain+` d ON d.id = e.domain_id `+where+` ORDER BY e.id`, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: list billing evidence")
	}
	defer func() { _ = rows.Close() }()
	out := make([]evidenceRef, 0)
	for rows.Next() {
		var r evidenceRef
		if err := rows.Scan(&r.id, &r.domain, &r.account, &r.key.Provider, &r.key.InstanceID,
			&r.key.Endpoint, &r.key.AuthRef, &r.key.CredentialFingerprint); err != nil {
			return nil, wrapScan(err, "billing_evidence")
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: read billing evidence")
	}
	return out, nil
}

// deleteEvidence deletes refs and demotes each affected paid domain that
// has no evidence left, all in one transaction.
func (s *Store) deleteEvidence(ctx context.Context, refs []evidenceRef) (int, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	err := s.withTx(ctx, func(tx *Store) error {
		domains := make(map[DomainID]bool)
		for _, r := range refs {
			if _, err := tx.db.ExecContext(ctx, `DELETE FROM `+tableBillingEvidence+` WHERE id = ?`, r.id); err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, "topology: delete billing evidence")
			}
			domains[r.domain] = true
		}
		for d := range domains {
			if _, err := tx.db.ExecContext(ctx, `UPDATE `+tableQuotaDomain+` SET billing_tier = ?
				WHERE id = ? AND billing_tier = ? AND NOT EXISTS (SELECT 1 FROM `+tableBillingEvidence+` WHERE domain_id = ?)`,
				string(BillingTierDiscover), string(d), string(BillingTierPaid), string(d)); err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, "topology: demote domain without evidence")
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(refs), nil
}

// validateEvidence refuses evidence that cannot justify promoting id.
func validateEvidence(id DomainID, ev []BillingEvidence) error {
	if !id.Valid() {
		return cascade.New(cascade.KindInvalidInput, "topology: PromoteDomain requires a valid domain id")
	}
	if len(ev) == 0 {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrBillingEvidenceRefused, "domain %q: no evidence rows", id)
	}
	for i, e := range ev {
		var why string
		switch {
		case e.DomainID != id:
			why = "names domain " + string(e.DomainID)
		case e.TestOutcome != EvidenceOutcomePassed:
			why = "test outcome is not " + EvidenceOutcomePassed
		case strings.TrimSpace(e.PromotedBy) == "":
			why = "has no PromotedBy"
		case e.Key.Provider == "":
			why = "has no provider"
		case e.ObservedAt.IsZero():
			why = "has no ObservedAt"
		default:
			continue
		}
		return cascade.Wrapf(cascade.KindInvalidInput, ErrBillingEvidenceRefused, "domain %q: evidence row %d %s", id, i, why)
	}
	return nil
}

// insertEvidence writes one evidence row.
func (s *Store) insertEvidence(ctx context.Context, e BillingEvidence) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableBillingEvidence+` (`+evidenceColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		string(e.DomainID), e.Key.Provider, e.Key.InstanceID, e.Key.Endpoint, e.Key.AuthRef, e.Key.CredentialFingerprint,
		e.PromotedBy, e.TestOutcome, e.ProbeKind, e.ProbeStatus, e.ProbeEndpoint, e.ObservedAt.UnixMilli())
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: insert billing evidence for %q", e.DomainID)
	}
	return nil
}

// setBillingTier writes domain id's billing_tier, or ErrTopologyNotFound.
func (s *Store) setBillingTier(ctx context.Context, id DomainID, tier BillingTier) error {
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableQuotaDomain+` SET billing_tier = ? WHERE id = ?`, string(tier), string(id))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: set billing tier for %q", id)
	}
	if n, err := res.RowsAffected(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "topology: billing tier rows affected")
	} else if n == 0 {
		return newNotFoundErr("quota_domain", string(id))
	}
	return nil
}
