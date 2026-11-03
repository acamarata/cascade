// Purpose: Lane CRUD (paired with store.go's Account/QuotaDomain/Credential
// CRUD), RetireLane, CheckReservable, the registry -> topology derivation
// helpers and Reconcile's transaction (Report, validation). The pass body
// and its reconcile-scoped writers live in reconcile_upsert.go. UpsertLane's ON
// CONFLICT clause never touches discovered_at/lane_class/base_shadow_price/
// roles/model_identity/offering_snapshot.
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

// Report summarizes one Reconcile pass. Every count is of rows the pass
// actually inserted or changed; a pass over unchanged inputs is all zero.
type Report struct {
	// ProvidersReconciled counts account rows inserted or changed.
	ProvidersReconciled int
	// LanesReconciled counts lane rows inserted or changed (un-retirements included).
	LanesReconciled int
	// RowsWritten counts every topology row inserted or changed, retirements included.
	RowsWritten int
	// Retired counts lanes retired because the registry no longer derives them.
	Retired int
	// Unretired counts retired lanes the registry derives again.
	Unretired int
	// Violations lists the invariant failures that aborted the pass.
	Violations []Violation
}

// Reconcile derives every registry provider/lane record into topology rows
// inside one transaction, retires the lanes it no longer derives and
// re-runs Validate before commit. On error the Report carries only the
// violations: no row of the pass was kept.
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
		report = Report{}
		desired := make(map[LaneID]Lane)
		for _, p := range providers {
			if err := reconcileProvider(ctx, tx, p, byProvider[p.Name], desired, &report); err != nil {
				return err
			}
		}
		if err := writeDesiredLanes(ctx, tx, clock, desired, &report); err != nil {
			return err
		}
		if err := retireVanishedLanes(ctx, tx, clock, desired, &report); err != nil {
			return err
		}
		return validatePass(ctx, tx, &report)
	})
	if txErr != nil {
		return Report{Violations: report.Violations}, txErr
	}
	return report, nil
}

func validatePass(ctx context.Context, tx *Store, report *Report) error {
	snap, err := tx.snapshot(ctx)
	if err != nil {
		return err
	}
	if violations := Validate(ctx, snap); len(violations) > 0 {
		report.Violations = violations
		return ErrTopologyInvariant
	}
	return nil
}
