// Purpose: the composition-root fix for the disclosed provider add/list
//   split (P1-E10-W3-S21-T2's journal, "TWO SEPARATE, disconnected
//   registries"): registryAdapter satisfies internal/providers/intake's
//   Registry seam over the SAME durable internal/providers/registry.
//   Registry that list/test/remove/health/usage already open
//   (openProviderStorage, provider_health_cmd.go), so `provider add`'s
//   writes are visible to every other subcommand in the SAME process and
//   after a fresh reopen of providers.db.
// Inputs: a *registry.Registry, already migrated.
// Outputs: intake.ProviderRecord values translated from/to
//   registry.ProviderRecord.
// Constraints: never invents a credential value -- every field this file
//   touches is a name, an enum, or a timestamp, matching both packages'
//   own VaultKeyRef discipline.
// SPORT: cli.provider.add/ADD (P1-E10-W3-S20-T1 follow-up).

package main

import (
	"context"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
)

// registryAdapter implements intake.Registry over the durable
// registry.Registry. intake.Add is the only production caller of
// UpsertProvider; its re-add-updates contract (provider_cmd.go's own
// `provider add` --help text: "Re-adding an existing name re-verifies and
// updates the record rather than creating a duplicate", proven by
// TestProviderAddKeyIdempotentAcrossInvocations) is preserved unchanged --
// this adapter's UpsertProvider merges onto the existing row rather than
// resetting it, so a re-add never clobbers registry-only fields (health,
// tier, account kind, demotion count) that intake itself does not own.
//
// Pool membership: registry.ProviderRecord has no Pool/PoolIndex field;
// the durable registry keeps membership on the provider's lane (lanes.go).
// --pool is persisted on that lane (provider_lane.go), and GetProvider and
// ListPool read Pool/PoolIndex back from it, so intake's join index sees
// every member's real index.
type registryAdapter struct {
	reg *registry.Registry
}

// newRegistryAdapter wraps reg as an intake.Registry.
func newRegistryAdapter(reg *registry.Registry) intake.Registry {
	return registryAdapter{reg: reg}
}

// UpsertProvider implements intake.Registry: merges rec's intake-owned
// fields onto the existing durable row (if any), preserving every
// registry-only field UpsertProvider's zero value would otherwise reset.
func (a registryAdapter) UpsertProvider(ctx context.Context, rec intake.ProviderRecord) error {
	merged := mergeIntakeOntoRegistryRecord(ctx, a.reg, rec)
	if err := a.reg.UpsertProvider(ctx, merged); err != nil {
		return err
	}
	// And its ROUTER LANE. Without this the provider row exists and every
	// dispatch still answers "no candidate lane", because the router
	// selects over lanes — the exact failure the W3 hardening gate found in
	// the tagged artifact. See provider_lane.go.
	return upsertProviderLane(ctx, a.reg, rec)
}

// GetProvider implements intake.Registry, translating the durable record
// back to intake's shape. Errors pass through unchanged -- registry.
// GetProvider already returns a KindNotFound-tagged error for a missing
// name, matching intake.Registry's own contract. Pool/PoolIndex are read
// back from the provider's own lane (P1-E38-W8-S123-T1): without them a
// read-modify-write (provider reauth) re-upserts the lane under the bare
// name, leaving a second lane beside the pooled one.
func (a registryAdapter) GetProvider(ctx context.Context, name string) (intake.ProviderRecord, error) {
	rec, err := a.reg.GetProvider(ctx, name)
	if err != nil {
		return intake.ProviderRecord{}, err
	}
	out := fromRegistryRecord(rec)
	lanes, err := a.reg.ListLanes(ctx)
	if err != nil {
		return intake.ProviderRecord{}, err
	}
	out.Pool, out.PoolIndex = poolMembershipFor(lanes, name)
	return out, nil
}

// poolMembershipFor returns the pool and index of name's pooled lane (the
// one laneNameFor names "<pool>/<name>"), or ("", 0) for a standalone one.
func poolMembershipFor(lanes []registry.LaneRecord, name string) (string, int) {
	for _, l := range lanes {
		if l.ProviderName == name && l.PoolMembership != "" && l.LaneName == l.PoolMembership+"/"+name {
			return l.PoolMembership, l.PoolIndex
		}
	}
	return "", 0
}

// ListPool implements intake.Registry: every provider whose own pooled
// lane (the one poolMembershipFor recognises) belongs to pool, in name
// order (registry.ListPool orders by provider_name), with Pool/PoolIndex
// read from that lane. Any read error is returned, never an empty list:
// intake would hand out an index a member already holds.
func (a registryAdapter) ListPool(ctx context.Context, pool string) ([]intake.ProviderRecord, error) {
	lanes, err := a.reg.ListPool(ctx, pool)
	if err != nil {
		return nil, err
	}
	out := make([]intake.ProviderRecord, 0, len(lanes))
	for _, l := range lanes {
		if l.LaneName != pool+"/"+l.ProviderName {
			continue
		}
		rec, err := a.reg.GetProvider(ctx, l.ProviderName)
		if err != nil {
			return nil, err
		}
		member := fromRegistryRecord(rec)
		member.Pool, member.PoolIndex = l.PoolMembership, l.PoolIndex
		out = append(out, member)
	}
	return out, nil
}

// mergeIntakeOntoRegistryRecord builds the registry.ProviderRecord
// UpsertProvider should write: intake's own fields overwrite, every
// registry-only field (AccountKind, Tier, HealthStatus, HealthCheckedAt,
// DemotionCount, Cost, CreatedAt) carries over from the existing row when
// one exists, or takes a disclosed default (AccountPersonal, TierMid,
// HealthUnknown) for a genuinely new provider -- 08-INIT-CONFIG-SPEC.md
// names no default for either enum on a CLI-added credential.
func mergeIntakeOntoRegistryRecord(ctx context.Context, reg *registry.Registry, rec intake.ProviderRecord) registry.ProviderRecord {
	out := registry.ProviderRecord{
		Name:                 rec.Name,
		Driver:               registry.DriverKind(rec.Driver),
		BaseURL:              rec.BaseURL,
		Auth:                 registry.AuthType(rec.Auth),
		AuthRef:              registry.VaultKeyRef(rec.AuthRef.String()),
		KnownModels:          rec.KnownModels,
		Capabilities:         rec.Capabilities,
		CapabilitiesProbedAt: rec.CapabilitiesProbedAt,
		AccountKind:          registry.AccountPersonal,
		Tier:                 registry.TierMid,
		HealthStatus:         registry.HealthUnknown,
	}
	if existing, err := reg.GetProvider(ctx, rec.Name); err == nil {
		out.AccountKind = existing.AccountKind
		out.Tier = existing.Tier
		out.HealthStatus = existing.HealthStatus
		out.HealthCheckedAt = existing.HealthCheckedAt
		out.DemotionCount = existing.DemotionCount
		out.Cost = existing.Cost
		out.CreatedAt = existing.CreatedAt
	}
	return out
}

// fromRegistryRecord translates a durable record back to intake's shape.
// Pool/PoolIndex live on the lane, not the record (GetProvider fills them);
// VerifySkipped has no registry-side counterpart and reads back false.
func fromRegistryRecord(rec registry.ProviderRecord) intake.ProviderRecord {
	return intake.ProviderRecord{
		Name:                 rec.Name,
		Driver:               intake.DriverKind(rec.Driver),
		BaseURL:              rec.BaseURL,
		Auth:                 intake.AuthType(rec.Auth),
		AuthRef:              intake.VaultKeyRef(rec.AuthRef.String()),
		KnownModels:          rec.KnownModels,
		Capabilities:         rec.Capabilities,
		CapabilitiesProbedAt: rec.CapabilitiesProbedAt,
		CreatedAt:            rec.CreatedAt,
		UpdatedAt:            rec.UpdatedAt,
	}
}
