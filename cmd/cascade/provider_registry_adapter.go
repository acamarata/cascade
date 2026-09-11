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
// KNOWN GAP, disclosed rather than forced: registry.ProviderRecord has no
// Pool/PoolIndex field -- pool membership in the durable registry lives on
// LaneRecord (lanes.go), a different shape than intake's simple
// ProviderRecord.Pool string. Folding intake's --pool flag onto the lane
// model (or adding the fields to registry.ProviderRecord) needs its own
// decision and is out of this fix's scope; ListPool below is therefore an
// honest stub (always empty) rather than a silent half-implementation, and
// --pool's PoolIndex is not persisted through this adapter. Nothing in
// this ticket's required properties exercises --pool.
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
	return a.reg.UpsertProvider(ctx, merged)
}

// GetProvider implements intake.Registry, translating the durable record
// back to intake's shape. Errors pass through unchanged -- registry.
// GetProvider already returns a KindNotFound-tagged error for a missing
// name, matching intake.Registry's own contract.
func (a registryAdapter) GetProvider(ctx context.Context, name string) (intake.ProviderRecord, error) {
	rec, err := a.reg.GetProvider(ctx, name)
	if err != nil {
		return intake.ProviderRecord{}, err
	}
	return fromRegistryRecord(rec), nil
}

// ListPool implements intake.Registry. See this file's header comment:
// pool membership has no home on registry.ProviderRecord yet, so this is
// an honest, documented stub rather than a fabricated result.
func (a registryAdapter) ListPool(context.Context, string) ([]intake.ProviderRecord, error) {
	return nil, nil
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
// Pool/PoolIndex/VerifySkipped have no registry-side counterpart (see this
// file's header comment) and read back as their zero values.
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
