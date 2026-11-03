// Purpose: the body of a Reconcile pass (reconcile.go opens its one
// transaction) and its reconcile-scoped writers. Each writer inserts a new row in full but, on conflict,
// refreshes only the columns Reconcile owns, and only when one differs, so
// a pass over unchanged inputs changes zero bytes and reports zero counts.
// Owner columns are insert-only: credential quota_domain_ref, health and
// quarantine; domain billing_tier and quarantined; profile config_home,
// persistent, endpoint and env_var; account role and reported spend.
// A lane under a quarantined credential or domain is written quarantined.
// A lane no longer derived from the registry is retired (never deleted);
// derived again, it is un-retired under the same LaneID. A violation or a
// write error aborts the transaction with the prior rows intact.
// SPORT: fleet/topology/reconcile/ADD (P1-TOP-06).

package topology

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// reconcileProvider writes one Account and every one of its lanes'
// QuotaDomain/RuntimeProfile/Credential rows, collecting the lanes.
func reconcileProvider(ctx context.Context, tx *Store, p registry.ProviderRecord, lanes []registry.LaneRecord, desired map[LaneID]Lane, report *Report) error {
	billing := billingKindFor(p, lanes)
	n, err := tx.reconcileAccount(ctx, Account{ID: AccountID(p.Name), Provider: string(p.Driver),
		Billing: BillingInfo{Kind: billing}, Role: AccountRoleWorkforce})
	if err != nil {
		return err
	}
	report.ProvidersReconciled += n
	report.RowsWritten += n
	for _, l := range lanes {
		if err := reconcileLane(ctx, tx, p, l, billing, desired, report); err != nil {
			return err
		}
	}
	return nil
}

// reconcileLane writes one registry lane's QuotaDomain, RuntimeProfile and
// Credential (CredentialID(lane name)), then derives its topology lane from
// the STORED credential and domain, so the owner's quota_domain_ref and
// quarantine decide the lane's domain and health.
func reconcileLane(ctx context.Context, tx *Store, p registry.ProviderRecord, l registry.LaneRecord, billing BillingKind, desired map[LaneID]Lane, report *Report) error {
	domainID := domainIDForBilling(billing, p.Name, l.PoolMembership)
	profileID := RuntimeProfileID(p.Name + ":" + string(p.Driver))
	credID := CredentialID(l.LaneName)
	writes := []func() (int, error){
		func() (int, error) {
			return tx.reconcileQuotaDomain(ctx, QuotaDomain{ID: domainID, AccountRef: AccountID(p.Name),
				Kind: domainKindForBilling(billing), BillingTier: BillingTierDiscover})
		},
		func() (int, error) {
			return tx.reconcileRuntimeProfile(ctx, RuntimeProfile{ID: profileID, Runtime: runtimeKindForDriver(p.Driver)})
		},
		func() (int, error) {
			return tx.reconcileCredential(ctx, Credential{ID: credID, AccountRef: AccountID(p.Name), QuotaDomainRef: domainID,
				SecretRef: VaultKeyRef(p.AuthRef.String()), RuntimeProfileRef: profileID, Health: CredentialOK})
		},
	}
	for _, w := range writes {
		n, err := w()
		if err != nil {
			return err
		}
		report.RowsWritten += n
	}
	cred, err := tx.GetCredential(ctx, credID)
	if err != nil {
		return err
	}
	domain, err := tx.GetQuotaDomain(ctx, cred.QuotaDomainRef)
	if err != nil {
		return err
	}
	lane := derivedLane(cred, domain, l)
	if prev, ok := desired[lane.ID]; ok && prev.Health == LaneHealthQuarantined {
		lane.Health = LaneHealthQuarantined
	}
	desired[lane.ID] = lane
	return nil
}

// derivedLane is the topology lane one registry lane maps to under its
// stored credential and domain.
func derivedLane(cred Credential, domain QuotaDomain, l registry.LaneRecord) Lane {
	modelID := ""
	if len(l.ModelFilter) == 1 {
		modelID = l.ModelFilter[0]
	}
	credID := cred.ID
	return Lane{ID: LaneIDFor(cred.RuntimeProfileRef, domain.ID, "", modelID, EffortNA, InteractionInteractive),
		RuntimeProfileRef: cred.RuntimeProfileRef, QuotaDomainRef: domain.ID, CredentialRef: &credID, ModelID: modelID,
		Effort: EffortNA, InteractionClass: InteractionInteractive, Health: laneHealthFor(l.State, cred, domain),
		LaneClass: LaneClassUnranked, BaseShadowPrice: 1.0}
}

// laneHealthFor: quarantined under a credential that is not ok or a
// quarantined domain; otherwise the registry state, and an unknown state
// reads as the strictest member, quarantined.
func laneHealthFor(state registry.LaneState, cred Credential, domain QuotaDomain) LaneHealth {
	if cred.Health != CredentialOK || domain.Quarantined {
		return LaneHealthQuarantined
	}
	if h := LaneHealth(state); h.Valid() {
		return h
	}
	return LaneHealthQuarantined
}

// writeDesiredLanes writes every derived lane in LaneID order, counting an
// un-retirement when the stored row was retired.
func writeDesiredLanes(ctx context.Context, tx *Store, clock Clock, desired map[LaneID]Lane, report *Report) error {
	ids := make([]LaneID, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		prev, err := tx.GetLane(ctx, id)
		wasRetired := err == nil && prev.RetiredAt != nil
		if err != nil && !cascade.HasKind(err, cascade.KindNotFound) {
			return err
		}
		lane := desired[id]
		lane.DiscoveredAt = clock.Now()
		n, err := tx.reconcileLaneRow(ctx, lane)
		if err != nil {
			return err
		}
		report.LanesReconciled += n
		report.RowsWritten += n
		if wasRetired && n > 0 {
			report.Unretired++
		}
	}
	return nil
}

// retireVanishedLanes retires every credentialed lane this pass did not
// derive (a nil-credential lane is enrolled by another writer). A retired
// lane under a quarantined domain is written quarantined, so the
// quarantine-propagation invariant holds for retired rows too.
func retireVanishedLanes(ctx context.Context, tx *Store, clock Clock, desired map[LaneID]Lane, report *Report) error {
	lanes, err := tx.ListLanes(ctx, true)
	if err != nil {
		return err
	}
	for _, l := range lanes {
		if _, keep := desired[l.ID]; keep || l.CredentialRef == nil {
			continue
		}
		domain, err := tx.GetQuotaDomain(ctx, l.QuotaDomainRef)
		if err != nil {
			return err
		}
		health := l.Health
		if domain.Quarantined {
			health = LaneHealthQuarantined
		}
		n, err := tx.retireLaneRow(ctx, l.ID, health, clock)
		if err != nil {
			return err
		}
		report.RowsWritten += n
		if l.RetiredAt == nil && n > 0 {
			report.Retired++
		}
	}
	return nil
}

// changedWhere is a DO UPDATE guard true only when one of cols differs, so
// an unchanged row is never rewritten and RowsAffected counts real changes.
func changedWhere(table string, cols ...string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += " OR "
		}
		out += table + "." + c + " IS NOT excluded." + c
	}
	return out
}

// execChanged runs one reconcile write and returns the rows it changed.
func (s *Store) execChanged(ctx context.Context, entity, id, query string, args ...any) (int, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindUnavailable, err, "topology: reconcile %s %q", entity, id)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindUnavailable, err, "topology: reconcile %s %q", entity, id)
	}
	return int(n), nil
}

// reconcileAccount inserts rec or refreshes provider and billing_kind;
// role and the reported monthly spend are insert-only.
func (s *Store) reconcileAccount(ctx context.Context, rec Account) (int, error) {
	if !rec.ID.Valid() || !rec.Billing.Kind.Valid() || !rec.Role.Valid() {
		return 0, cascade.New(cascade.KindInvalidInput, "topology: invalid account")
	}
	return s.execChanged(ctx, "account", string(rec.ID), `INSERT INTO `+tableAccount+` (`+accountColumns+`) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET provider=excluded.provider, billing_kind=excluded.billing_kind
		WHERE `+changedWhere(tableAccount, "provider", "billing_kind"),
		string(rec.ID), rec.Provider, string(rec.Billing.Kind), rec.Billing.UserReportedMonthlyMicros, string(rec.Role))
}

// reconcileQuotaDomain inserts rec or refreshes account_ref and kind;
// billing_tier and quarantined are insert-only.
func (s *Store) reconcileQuotaDomain(ctx context.Context, rec QuotaDomain) (int, error) {
	if !rec.ID.Valid() || !rec.Kind.Valid() || !rec.BillingTier.Valid() {
		return 0, cascade.New(cascade.KindInvalidInput, "topology: invalid quota domain")
	}
	return s.execChanged(ctx, "quota domain", string(rec.ID), `INSERT INTO `+tableQuotaDomain+` (`+quotaDomainColumns+`) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET account_ref=excluded.account_ref, kind=excluded.kind
		WHERE `+changedWhere(tableQuotaDomain, "account_ref", "kind"),
		string(rec.ID), string(rec.AccountRef), string(rec.Kind), string(rec.BillingTier), boolToInt(rec.Quarantined))
}

// reconcileRuntimeProfile inserts rec or refreshes runtime; config_home,
// persistent, the endpoint and env_var are insert-only.
func (s *Store) reconcileRuntimeProfile(ctx context.Context, rec RuntimeProfile) (int, error) {
	if !rec.ID.Valid() || !rec.Runtime.Valid() {
		return 0, cascade.New(cascade.KindInvalidInput, "topology: invalid runtime profile")
	}
	return s.execChanged(ctx, "runtime profile", string(rec.ID), `INSERT INTO `+tableRuntimeProfile+` (`+runtimeProfileColumns+`) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET runtime=excluded.runtime WHERE `+changedWhere(tableRuntimeProfile, "runtime"),
		string(rec.ID), string(rec.Runtime), rec.ConfigHome, boolToInt(rec.Persistent), rec.Endpoint.Host, rec.Endpoint.Port, rec.EnvVar)
}

// reconcileCredential inserts rec or refreshes account_ref, secret_ref and
// runtime_profile_ref; quota_domain_ref (R28 B3), health and the quarantine
// columns are insert-only.
func (s *Store) reconcileCredential(ctx context.Context, rec Credential) (int, error) {
	if !rec.ID.Valid() || !rec.QuotaDomainRef.Valid() || !rec.RuntimeProfileRef.Valid() || !rec.Health.Valid() {
		return 0, cascade.New(cascade.KindInvalidInput, "topology: invalid credential")
	}
	return s.execChanged(ctx, "credential", string(rec.ID), `INSERT INTO `+tableCredential+` (`+credentialColumns+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET account_ref=excluded.account_ref, secret_ref=excluded.secret_ref,
			runtime_profile_ref=excluded.runtime_profile_ref
		WHERE `+changedWhere(tableCredential, "account_ref", "secret_ref", "runtime_profile_ref"),
		string(rec.ID), string(rec.AccountRef), string(rec.QuotaDomainRef), rec.SecretRef.String(),
		string(rec.RuntimeProfileRef), string(rec.Health), rec.QuarantineReason, unixOrNil(rec.QuarantinedUntil))
}

// reconcileLaneRow inserts rec in full or refreshes the columns Reconcile
// derives and clears retired_at (un-retire, same LaneID); discovered_at,
// lane_class, base_shadow_price, roles, model identity and the offering
// snapshot survive untouched.
func (s *Store) reconcileLaneRow(ctx context.Context, rec Lane) (int, error) {
	if !rec.ID.Valid() || !rec.RuntimeProfileRef.Valid() || !rec.QuotaDomainRef.Valid() || !rec.Health.Valid() {
		return 0, cascade.New(cascade.KindInvalidInput, "topology: invalid lane")
	}
	rolesJSON, err := encodeRoles(rec.Roles)
	if err != nil {
		return 0, err
	}
	snapJSON, err := encodeOfferingSnapshot(rec.OfferingSnapshot)
	if err != nil {
		return 0, err
	}
	cols := []string{"runtime_profile_ref", "quota_domain_ref", "credential_ref", "model_id", "effort", "interaction_class", "health"}
	return s.execChanged(ctx, "lane", string(rec.ID), `INSERT INTO `+tableLane+` (`+laneColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET runtime_profile_ref=excluded.runtime_profile_ref,
			quota_domain_ref=excluded.quota_domain_ref, credential_ref=excluded.credential_ref,
			model_id=excluded.model_id, effort=excluded.effort, interaction_class=excluded.interaction_class,
			health=excluded.health, retired_at=NULL
		WHERE `+changedWhere(tableLane, cols...)+` OR `+tableLane+`.retired_at IS NOT NULL`,
		string(rec.ID), string(rec.RuntimeProfileRef), string(rec.QuotaDomainRef), credRefValue(rec.CredentialRef),
		rec.ModelID, string(rec.Effort), rolesJSON, string(rec.LaneClass), string(rec.InteractionClass),
		rec.BaseShadowPrice, string(rec.Health), rec.ModelIdentity.CanonicalID, rec.ModelIdentity.Family,
		snapJSON, rec.OfferingSnapshot.Version, rec.DiscoveredAt.UnixMilli(), nil)
}

// retireLaneRow stamps retired_at (kept when already set; the row is never
// deleted) and writes health; an unchanged row is not rewritten.
func (s *Store) retireLaneRow(ctx context.Context, id LaneID, health LaneHealth, clock Clock) (int, error) {
	return s.execChanged(ctx, "lane", string(id), `UPDATE `+tableLane+` SET retired_at = COALESCE(retired_at, ?), health = ?
		WHERE id = ? AND (retired_at IS NULL OR health IS NOT ?)`,
		clock.Now().UnixMilli(), string(health), string(id), string(health))
}
