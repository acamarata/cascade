//go:build !windows

// Purpose: the production lane tier cap of the daemon's conductor router: the
// topology data-class ceiling of each registry lane, with the paid-evidence
// rule bound to the one billing evidence key function. The sensitivity table
// seam is bound by the daemon's fleet step (internal/daemon).
// Inputs: the fleet runtime's topology store, the effective restricted
// permits, and the one evidence key function (newEvidenceKeyFn).
// Outputs: conductor.WithLaneTierCap options and the topology seam
// PaidEvidenceCurrent, which the cap and the fleet ceiling floor both read.
// Constraints: fail closed. No topology store, a ceiling read error, missing,
// unequal or unreadable billing evidence never raises a lane above public or
// admits anything. There is no admit-all cap and no bypass.
// SPORT: cmd/cascade daemon conductor wiring (ADD).
package main

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fleetTopology returns the fleet runtime's topology store, or nil for a
// wiring built without a fleet runtime.
func fleetTopology(f *daemon.FleetRuntime) *topology.Store {
	if f == nil {
		return nil
	}
	return f.Topology()
}

// topologyLaneTierCap is the production conductor.LaneTierCap: lane's
// ceiling in s under its lane type and restricted permit, with the ceiling
// reason. Without a store every read is an error, which admits nothing.
func topologyLaneTierCap(s *topology.Store, permits conductor.RestrictedPermits) conductor.LaneTierCap {
	read := capacity.RegistryLaneCeilings(s, permits)
	return func(ctx context.Context, lane conductor.LaneID, lt conductor.LaneType) (provider.SensitivityTier, string, error) {
		if read == nil {
			return provider.SensitivityLocalOnly, "", cascade.New(cascade.KindUnavailable, "lane tier cap: no topology store")
		}
		c, err := read(ctx, lane, lt)
		if err != nil {
			return provider.SensitivityLocalOnly, "", err
		}
		return c.Tier, string(c.Reason), nil
	}
}

// laneTierCapOption is the router option that carries the production cap.
func laneTierCapOption(s *topology.Store, permits conductor.RestrictedPermits) conductor.RouterOption {
	return conductor.WithLaneTierCap(topologyLaneTierCap(s, permits))
}

// refuseLaneTierCap is the router's default cap: it errors for every lane,
// so a router wired without the production cap admits nothing.
func refuseLaneTierCap(context.Context, conductor.LaneID, conductor.LaneType) (provider.SensitivityTier, string, error) {
	return provider.SensitivityLocalOnly, "", cascade.New(cascade.KindUnavailable, "lane tier cap: the topology cap is not wired")
}

// paidEvidenceCurrent returns the topology PaidEvidenceCurrent rule over
// key: a paid domain is current only when it has stored evidence and every
// row equals its account's current key. A gone key (KindNotFound), no rows
// or a changed key is not current; any other error refuses the read.
func paidEvidenceCurrent(key topology.EvidenceKeyFn) func(context.Context, *topology.Store, topology.QuotaDomain) (bool, error) {
	return func(ctx context.Context, s *topology.Store, d topology.QuotaDomain) (bool, error) {
		if key == nil {
			return false, nil
		}
		rows, err := s.BillingEvidenceFor(ctx, d.ID)
		if err != nil || len(rows) == 0 {
			return false, err
		}
		cur, err := key(ctx, d.AccountRef)
		switch {
		case cascade.HasKind(err, cascade.KindNotFound):
			return false, nil
		case err != nil:
			return false, err
		}
		for _, r := range rows {
			if r.Key != cur {
				return false, nil
			}
		}
		return true, nil
	}
}

// bindPaidEvidence binds topology.PaidEvidenceCurrent to the stored billing
// evidence and newEvidenceKeyFn, built over the inputs newDaemonResolver
// handed to the domain promote (the same key function inputs, the same key
// file). It runs after newDaemonResolver; a key function that cannot be
// built (a bad key file) fails the conductor wiring.
func bindPaidEvidence(paths runtime.PathProvider) error {
	v := promoteHandoff.Load()
	if v == nil {
		return cascade.New(cascade.KindUnavailable, "lane tier cap: no evidence key inputs")
	}
	keys := v.keys
	keys.KeyPath = filepath.Join(paths.DataDir(), "billing-evidence.key")
	key, err := newEvidenceKeyFn(keys)
	if err != nil {
		return err
	}
	topology.PaidEvidenceCurrent = paidEvidenceCurrent(key)
	return nil
}
