// Purpose: Upsert/Get/List CRUD for Account, QuotaDomain, RuntimeProfile
//
//	and Credential over the execer/Store seam schema.go declares (the
//	tree's own registry.go precedent for a multi-column relational
//	entity -- see this ticket's journal for why pkg/provider's namespace/
//	key/value Store family is not used here). Lane CRUD, RetireLane,
//	CheckReservable and Reconcile itself live in reconcile.go, which
//	needs one transaction spanning every entity.
//
// Inputs: context.Context plus one entity value per write call.
// Outputs: the persisted entity, or a *cascade.Error.
// Constraints: every write validates its closed-vocabulary fields before
//
//	reaching SQL; timestamps are caller-supplied (Art.7.3).
//
// SPORT: fleet/topology/store/ADD (P1-E40-W9-S77-T1).

package topology

import (
	"context"
	"database/sql"

	"github.com/acamarata/cascade/pkg/cascade"
)

const accountColumns = `id, provider, billing_kind, billing_user_reported_monthly_micros, role`

// UpsertAccount inserts or updates rec by rec.ID.
func (s *Store) UpsertAccount(ctx context.Context, rec Account) error {
	if !rec.ID.Valid() || !rec.Billing.Kind.Valid() || !rec.Role.Valid() {
		return cascade.New(cascade.KindInvalidInput, "topology: invalid account")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableAccount+` (`+accountColumns+`) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET provider=excluded.provider, billing_kind=excluded.billing_kind,
			billing_user_reported_monthly_micros=excluded.billing_user_reported_monthly_micros, role=excluded.role`,
		string(rec.ID), rec.Provider, string(rec.Billing.Kind), rec.Billing.UserReportedMonthlyMicros, string(rec.Role))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: upsert account %q", rec.ID)
	}
	return nil
}

// GetAccount returns the account named id, or ErrTopologyNotFound.
func (s *Store) GetAccount(ctx context.Context, id AccountID) (Account, error) {
	return getRow(ctx, s.db, `SELECT `+accountColumns+` FROM `+tableAccount+` WHERE id = ?`,
		[]any{string(id)}, scanAccount, newNotFoundErr("account", string(id)))
}

// ListAccounts returns every account, ordered by id.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	return listRows(ctx, s.db, `SELECT `+accountColumns+` FROM `+tableAccount+` ORDER BY id`, scanAccount)
}

func scanAccount(row rowScanner) (Account, error) {
	var rec Account
	var billingKind, role string
	if err := row.Scan(&rec.ID, &rec.Provider, &billingKind, &rec.Billing.UserReportedMonthlyMicros, &role); err != nil {
		return Account{}, wrapScan(err, "account")
	}
	rec.Billing.Kind, rec.Role = BillingKind(billingKind), AccountRole(role)
	return rec, nil
}

const quotaDomainColumns = `id, account_ref, kind, billing_tier, quarantined`

// UpsertQuotaDomain inserts or updates rec by rec.ID.
func (s *Store) UpsertQuotaDomain(ctx context.Context, rec QuotaDomain) error {
	if !rec.ID.Valid() || !rec.Kind.Valid() || !rec.BillingTier.Valid() {
		return cascade.New(cascade.KindInvalidInput, "topology: invalid quota domain")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableQuotaDomain+` (`+quotaDomainColumns+`) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET account_ref=excluded.account_ref, kind=excluded.kind,
			billing_tier=excluded.billing_tier, quarantined=excluded.quarantined`,
		string(rec.ID), string(rec.AccountRef), string(rec.Kind), string(rec.BillingTier), boolToInt(rec.Quarantined))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: upsert quota domain %q", rec.ID)
	}
	return nil
}

// GetQuotaDomain returns the domain named id, or ErrTopologyNotFound.
func (s *Store) GetQuotaDomain(ctx context.Context, id DomainID) (QuotaDomain, error) {
	return getRow(ctx, s.db, `SELECT `+quotaDomainColumns+` FROM `+tableQuotaDomain+` WHERE id = ?`,
		[]any{string(id)}, scanQuotaDomain, newNotFoundErr("quota_domain", string(id)))
}

// ListQuotaDomains returns every quota domain, ordered by id.
func (s *Store) ListQuotaDomains(ctx context.Context) ([]QuotaDomain, error) {
	return listRows(ctx, s.db, `SELECT `+quotaDomainColumns+` FROM `+tableQuotaDomain+` ORDER BY id`, scanQuotaDomain)
}

func scanQuotaDomain(row rowScanner) (QuotaDomain, error) {
	var rec QuotaDomain
	var kind, tier string
	var quarantined int
	if err := row.Scan(&rec.ID, &rec.AccountRef, &kind, &tier, &quarantined); err != nil {
		return QuotaDomain{}, wrapScan(err, "quota_domain")
	}
	rec.Kind, rec.BillingTier, rec.Quarantined = QuotaDomainKind(kind), BillingTier(tier), quarantined != 0
	return rec, nil
}

const runtimeProfileColumns = `id, runtime, config_home, persistent, endpoint_host, endpoint_port, env_var`

// UpsertRuntimeProfile inserts or updates rec by rec.ID.
func (s *Store) UpsertRuntimeProfile(ctx context.Context, rec RuntimeProfile) error {
	if !rec.ID.Valid() || !rec.Runtime.Valid() {
		return cascade.New(cascade.KindInvalidInput, "topology: invalid runtime profile")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableRuntimeProfile+` (`+runtimeProfileColumns+`) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET runtime=excluded.runtime, config_home=excluded.config_home,
			persistent=excluded.persistent, endpoint_host=excluded.endpoint_host,
			endpoint_port=excluded.endpoint_port, env_var=excluded.env_var`,
		string(rec.ID), string(rec.Runtime), rec.ConfigHome, boolToInt(rec.Persistent),
		rec.Endpoint.Host, rec.Endpoint.Port, rec.EnvVar)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: upsert runtime profile %q", rec.ID)
	}
	return nil
}

// GetRuntimeProfile returns the profile named id, or ErrTopologyNotFound.
func (s *Store) GetRuntimeProfile(ctx context.Context, id RuntimeProfileID) (RuntimeProfile, error) {
	return getRow(ctx, s.db, `SELECT `+runtimeProfileColumns+` FROM `+tableRuntimeProfile+` WHERE id = ?`,
		[]any{string(id)}, scanRuntimeProfile, newNotFoundErr("runtime_profile", string(id)))
}

// ListRuntimeProfiles returns every runtime profile, ordered by id.
func (s *Store) ListRuntimeProfiles(ctx context.Context) ([]RuntimeProfile, error) {
	return listRows(ctx, s.db, `SELECT `+runtimeProfileColumns+` FROM `+tableRuntimeProfile+` ORDER BY id`, scanRuntimeProfile)
}

func scanRuntimeProfile(row rowScanner) (RuntimeProfile, error) {
	var rec RuntimeProfile
	var runtime string
	var persistent int
	err := row.Scan(&rec.ID, &runtime, &rec.ConfigHome, &persistent, &rec.Endpoint.Host, &rec.Endpoint.Port, &rec.EnvVar)
	if err != nil {
		return RuntimeProfile{}, wrapScan(err, "runtime_profile")
	}
	rec.Runtime, rec.Persistent = RuntimeKind(runtime), persistent != 0
	return rec, nil
}

const credentialColumns = `id, account_ref, quota_domain_ref, secret_ref, runtime_profile_ref, health, quarantine_reason, quarantined_until`

// UpsertCredential inserts or updates rec by rec.ID.
func (s *Store) UpsertCredential(ctx context.Context, rec Credential) error {
	if !rec.ID.Valid() || !rec.QuotaDomainRef.Valid() || !rec.RuntimeProfileRef.Valid() || !rec.Health.Valid() {
		return cascade.New(cascade.KindInvalidInput, "topology: invalid credential")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+tableCredential+` (`+credentialColumns+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET account_ref=excluded.account_ref, quota_domain_ref=excluded.quota_domain_ref,
			secret_ref=excluded.secret_ref, runtime_profile_ref=excluded.runtime_profile_ref, health=excluded.health,
			quarantine_reason=excluded.quarantine_reason, quarantined_until=excluded.quarantined_until`,
		string(rec.ID), string(rec.AccountRef), string(rec.QuotaDomainRef), rec.SecretRef.String(),
		string(rec.RuntimeProfileRef), string(rec.Health), rec.QuarantineReason, unixOrNil(rec.QuarantinedUntil))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: upsert credential %q", rec.ID)
	}
	return nil
}

// GetCredential returns the credential named id, or ErrTopologyNotFound.
func (s *Store) GetCredential(ctx context.Context, id CredentialID) (Credential, error) {
	return getRow(ctx, s.db, `SELECT `+credentialColumns+` FROM `+tableCredential+` WHERE id = ?`,
		[]any{string(id)}, scanCredential, newNotFoundErr("credential", string(id)))
}

// ListCredentials returns every credential, ordered by id.
func (s *Store) ListCredentials(ctx context.Context) ([]Credential, error) {
	return listRows(ctx, s.db, `SELECT `+credentialColumns+` FROM `+tableCredential+` ORDER BY id`, scanCredential)
}

func scanCredential(row rowScanner) (Credential, error) {
	var rec Credential
	var secretRef, health string
	var quarantinedUntil sql.NullInt64
	err := row.Scan(&rec.ID, &rec.AccountRef, &rec.QuotaDomainRef, &secretRef, &rec.RuntimeProfileRef,
		&health, &rec.QuarantineReason, &quarantinedUntil)
	if err != nil {
		return Credential{}, wrapScan(err, "credential")
	}
	rec.SecretRef, rec.Health = VaultKeyRef(secretRef), CredentialHealth(health)
	rec.QuarantinedUntil = nullUnixToTime(quarantinedUntil)
	return rec, nil
}
