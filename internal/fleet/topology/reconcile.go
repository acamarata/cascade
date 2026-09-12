// Purpose: Lane CRUD (paired with store.go's Account/QuotaDomain/Credential
// CRUD), RetireLane, CheckReservable, and Reconcile(ctx), rewriting
// J/S-20.T2 records into topology rows in one transaction. UpsertLane's ON
// CONFLICT clause never touches discovered_at/lane_class/base_shadow_price/
// roles/model_identity/offering_snapshot, so an unconditional re-upsert of
// unchanged input still writes byte-identical rows. A violation aborts the
// transaction with ErrTopologyInvariant, prior rows intact.
// SPORT: fleet/topology/reconcile/ADD (P1-E40-W9-S77-T1).

package topology

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Clock abstracts the wall clock (Art.7.3).
type Clock interface{ Now() time.Time }

const laneColumns = `id, runtime_profile_ref, quota_domain_ref, credential_ref, model_id, effort, roles,
	lane_class, interaction_class, base_shadow_price, health, model_identity_canonical_id,
	model_identity_family, offering_snapshot, offering_snapshot_version, discovered_at, retired_at`

// UpsertLane inserts a NEW lane with rec's full fields, or on conflict
// updates only the fields Reconcile derives fresh each run -- lane_class,
// base_shadow_price, roles, model_identity, offering_snapshot,
// discovered_at and retired_at survive untouched (S-77.T5/AO-S-80.T1 own them).
func (s *Store) UpsertLane(ctx context.Context, rec Lane) error {
	if !rec.ID.Valid() || !rec.RuntimeProfileRef.Valid() || !rec.QuotaDomainRef.Valid() || !rec.Health.Valid() {
		return cascade.New(cascade.KindInvalidInput, "topology: invalid lane")
	}
	rolesJSON, err := encodeRoles(rec.Roles)
	if err != nil {
		return err
	}
	snapJSON, err := encodeOfferingSnapshot(rec.OfferingSnapshot)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+tableLane+` (`+laneColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET runtime_profile_ref=excluded.runtime_profile_ref,
			quota_domain_ref=excluded.quota_domain_ref, credential_ref=excluded.credential_ref,
			model_id=excluded.model_id, effort=excluded.effort, interaction_class=excluded.interaction_class,
			health=excluded.health`,
		string(rec.ID), string(rec.RuntimeProfileRef), string(rec.QuotaDomainRef), credRefValue(rec.CredentialRef),
		rec.ModelID, string(rec.Effort), rolesJSON, string(rec.LaneClass), string(rec.InteractionClass),
		rec.BaseShadowPrice, string(rec.Health), rec.ModelIdentity.CanonicalID, rec.ModelIdentity.Family,
		snapJSON, rec.OfferingSnapshot.Version, rec.DiscoveredAt.UnixMilli(), unixOrNil(derefTime(rec.RetiredAt)))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: upsert lane %q", rec.ID)
	}
	return nil
}

// GetLane returns the lane named id, retired or not, or ErrTopologyNotFound.
func (s *Store) GetLane(ctx context.Context, id LaneID) (Lane, error) {
	return getRow(ctx, s.db, `SELECT `+laneColumns+` FROM `+tableLane+` WHERE id = ?`,
		[]any{string(id)}, scanLane, newNotFoundErr("lane", string(id)))
}

// ListLanes returns every lane, ordered by id; retired rows only if includeRetired.
func (s *Store) ListLanes(ctx context.Context, includeRetired bool) ([]Lane, error) {
	query := `SELECT ` + laneColumns + ` FROM ` + tableLane
	if !includeRetired {
		query += ` WHERE retired_at IS NULL`
	}
	return listRows(ctx, s.db, query+` ORDER BY id`, scanLane)
}

// RetireLane sets retired_at on the lane named id (row never deleted); a
// second retire is ErrTopologyConflict, never a silent re-stamp.
func (s *Store) RetireLane(ctx context.Context, id LaneID, retiredAt time.Time) error {
	cur, err := s.GetLane(ctx, id)
	if err != nil {
		return err
	}
	if cur.RetiredAt != nil {
		return cascade.Wrapf(cascade.KindConflict, ErrTopologyConflict, "lane %q is already retired", id)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableLane+` SET retired_at = ? WHERE id = ?`, retiredAt.UnixMilli(), string(id))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: retire lane %q", id)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return newNotFoundErr("lane", string(id))
	}
	return nil
}

// CheckReservable returns ErrLaneRetired for a retired lane, or
// ErrTopologyNotFound; nil means a NEW reservation may proceed (in-flight
// ones are unaffected). For AO/S-80's scheduler (R-21.124).
func (s *Store) CheckReservable(ctx context.Context, id LaneID) error {
	lane, err := s.GetLane(ctx, id)
	if err != nil {
		return err
	}
	if lane.RetiredAt != nil {
		return ErrLaneRetired
	}
	return nil
}

func credRefValue(ref *CredentialID) any {
	if ref == nil {
		return nil
	}
	return string(*ref)
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func scanLane(row rowScanner) (Lane, error) {
	var rec Lane
	var credRef sql.NullString
	var effort, roles, laneClass, interactionClass, health, canonicalID, family, snapJSON string
	var discoveredAt int64
	var retiredAt sql.NullInt64
	err := row.Scan(&rec.ID, &rec.RuntimeProfileRef, &rec.QuotaDomainRef, &credRef, &rec.ModelID, &effort, &roles,
		&laneClass, &interactionClass, &rec.BaseShadowPrice, &health, &canonicalID, &family, &snapJSON,
		&rec.OfferingSnapshot.Version, &discoveredAt, &retiredAt)
	if err != nil {
		return Lane{}, wrapScan(err, "lane")
	}
	if credRef.Valid {
		id := CredentialID(credRef.String)
		rec.CredentialRef = &id
	}
	rec.Effort, rec.LaneClass = Effort(effort), LaneClass(laneClass)
	rec.InteractionClass, rec.Health = InteractionClass(interactionClass), LaneHealth(health)
	if rec.Roles, err = decodeRoles(roles); err != nil {
		return Lane{}, err
	}
	rec.ModelIdentity = ModelIdentity{CanonicalID: canonicalID, Family: family}
	if err := decodeOfferingSnapshot(snapJSON, &rec.OfferingSnapshot); err != nil {
		return Lane{}, err
	}
	rec.DiscoveredAt = time.UnixMilli(discoveredAt)
	if retiredAt.Valid {
		t := time.UnixMilli(retiredAt.Int64)
		rec.RetiredAt = &t
	}
	return rec, nil
}

// Report summarizes one Reconcile pass.
type Report struct {
	ProvidersReconciled int
	LanesReconciled     int
	Violations          []Violation
}

// Reconcile rewrites every J/S-20.T2 provider/lane record into topology
// rows inside one transaction, re-running Validate before commit. Every
// derived row is upserted unconditionally -- see this file's header
// comment for why that is still idempotent.
func Reconcile(ctx context.Context, store *Store, reg *registry.Registry, clock Clock) (Report, error) {
	providers, err := reg.ListProviders(ctx)
	if err != nil {
		return Report{}, err
	}
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		return Report{}, err
	}
	byProvider := make(map[string][]registry.LaneRecord, len(providers))
	for _, l := range lanes {
		byProvider[l.ProviderName] = append(byProvider[l.ProviderName], l)
	}

	var report Report
	txErr := store.withTx(ctx, func(tx *Store) error {
		for _, p := range providers {
			if err := reconcileProvider(ctx, tx, clock, p, byProvider[p.Name], &report); err != nil {
				return err
			}
		}
		snap, err := tx.snapshot(ctx)
		if err != nil {
			return err
		}
		if violations := Validate(ctx, snap); len(violations) > 0 {
			report.Violations = violations
			return ErrTopologyInvariant
		}
		return nil
	})
	return report, txErr
}

// reconcileProvider derives/upserts one Account plus every one of its
// lanes' QuotaDomain/RuntimeProfile/Credential/Lane rows.
func reconcileProvider(ctx context.Context, tx *Store, clock Clock, p registry.ProviderRecord, lanes []registry.LaneRecord, report *Report) error {
	billing := billingKindFor(p, lanes)
	account := Account{ID: AccountID(p.Name), Provider: string(p.Driver), Billing: BillingInfo{Kind: billing}, Role: AccountRoleWorkforce}
	if err := tx.UpsertAccount(ctx, account); err != nil {
		return err
	}
	report.ProvidersReconciled++
	for _, l := range lanes {
		if err := reconcileLane(ctx, tx, clock, p, l, billing, report); err != nil {
			return err
		}
	}
	return nil
}

// billingKindFor: pool when any lane has a non-empty PoolMembership, else
// subscription for oauth auth, else api.
func billingKindFor(p registry.ProviderRecord, lanes []registry.LaneRecord) BillingKind {
	for _, l := range lanes {
		if l.PoolMembership != "" {
			return BillingPool
		}
	}
	if p.Auth == registry.AuthOAuth {
		return BillingSubscription
	}
	return BillingAPI
}

// runtimeKindForDriver is this ticket's T1 default: registry.DriverKind
// carries no CLI-harness distinction, so every driver maps to RuntimeAPI
// except ollama; AN/S-77.T2 enriches a profile once that association is
// known (contract-vs-tree note in this ticket's journal).
func runtimeKindForDriver(d registry.DriverKind) RuntimeKind {
	if d == registry.DriverOllama {
		return RuntimeOllama
	}
	return RuntimeAPI
}

func domainKindForBilling(b BillingKind) QuotaDomainKind {
	switch b {
	case BillingPool:
		return QuotaDomainSharedPool
	case BillingSubscription:
		return QuotaDomainSubscriptionWindow
	case BillingAPI, BillingFree:
		return QuotaDomainAPIProject
	default:
		return QuotaDomainAPIProject
	}
}

func domainIDForBilling(b BillingKind, providerName, poolMembership string) DomainID {
	switch b {
	case BillingPool:
		return DomainID("pool:" + poolMembership)
	case BillingSubscription:
		return DomainID(providerName + ":subscription")
	case BillingAPI, BillingFree:
		return DomainID(providerName + ":api")
	default:
		return DomainID(providerName + ":api")
	}
}

// reconcileLane derives/upserts one lane's QuotaDomain and RuntimeProfile,
// then its Credential and Lane rows: RuntimeProfile is upserted before
// Credential/Lane so "one profile per lane" holds on the FIRST run.
func reconcileLane(ctx context.Context, tx *Store, clock Clock, p registry.ProviderRecord, l registry.LaneRecord, billing BillingKind, report *Report) error {
	domainID := domainIDForBilling(billing, p.Name, l.PoolMembership)
	domain := QuotaDomain{ID: domainID, AccountRef: AccountID(p.Name), Kind: domainKindForBilling(billing), BillingTier: BillingTierDiscover}
	if err := tx.UpsertQuotaDomain(ctx, domain); err != nil {
		return err
	}

	profileID := RuntimeProfileID(p.Name + ":" + string(p.Driver))
	profile := RuntimeProfile{ID: profileID, Runtime: runtimeKindForDriver(p.Driver)}
	if err := tx.UpsertRuntimeProfile(ctx, profile); err != nil {
		return err
	}

	credID := CredentialID(l.LaneName)
	cred := Credential{ID: credID, AccountRef: AccountID(p.Name), QuotaDomainRef: domainID, SecretRef: VaultKeyRef(p.AuthRef.String()), RuntimeProfileRef: profileID, Health: CredentialOK}
	if err := tx.UpsertCredential(ctx, cred); err != nil {
		return err
	}

	modelID := ""
	if len(l.ModelFilter) == 1 {
		modelID = l.ModelFilter[0]
	}
	lane := Lane{ID: LaneIDFor(profileID, domainID, "", modelID, EffortNA, InteractionInteractive),
		RuntimeProfileRef: profileID, QuotaDomainRef: domainID, CredentialRef: &credID, ModelID: modelID,
		Effort: EffortNA, InteractionClass: InteractionInteractive, Health: LaneHealth(l.State),
		LaneClass: LaneClassUnranked, BaseShadowPrice: 1.0, DiscoveredAt: clock.Now()}
	report.LanesReconciled++
	return tx.UpsertLane(ctx, lane)
}
