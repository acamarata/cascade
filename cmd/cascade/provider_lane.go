// Purpose: gives every registered provider a ROUTER LANE, so a dispatch
//
//	has something to select.
//
// WHY THIS FILE EXISTS. The W3 hardening gate ran the tagged artifact:
//
//	`cascade provider add` against a real endpoint succeeded, and the very
//	next `cascade run` answered "conductor: no candidate lane". The router
//	selects over LANES (DefaultRouter.buildSnapshot reads ListLanes), and
//	registry.UpsertLane had NO production caller anywhere in the tree — so
//	the lane table was empty on every machine, for every provider, and no
//	dispatch could ever be routed. Same class as the unmounted commands and
//	the unwired security pipeline this gate also found: a write path whose
//	only callers were tests.
//
// Inputs: the durable registry and the provider record intake just wrote.
// Outputs: one upserted lane per provider.
// Constraints: the lane's STATE is evidence, not optimism. A provider whose
//
//	live micro-verify passed is `available`; one added with --no-verify is
//	`unknown`, because nothing has proved it answers. Neither is filtered
//	out by the router today (it evicts on PROVIDER health), so this records
//	what is known rather than asserting what is not.
//
// SPORT: cli.provider.add:lane (ADD) — P1-E10-W4-S87-T1 (Art.9, W3 gate).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
)

// laneNameFor is the lane a provider's own dispatches run on.
//
// A pooled provider's lane is namespaced by its pool so two providers in
// the same pool cannot collide on lane_name (the table's primary key),
// which would silently make the second add overwrite the first's lane.
func laneNameFor(rec intake.ProviderRecord) string {
	if rec.Pool != "" {
		return rec.Pool + "/" + rec.Name
	}
	return rec.Name
}

// laneCapacityFor maps the credential's auth type to its R-16.10 capacity
// bucket.
//
// An API key draws on api_credit; an OAuth/subscription credential draws
// on the interactive usage window. These are the only two intake can
// resolve — agent_sdk_credit is a property of how a lane is DRIVEN, not of
// how it authenticates, so intake never assigns it.
func laneCapacityFor(rec intake.ProviderRecord) registry.CapacityBucket {
	if rec.Auth == intake.AuthOAuth {
		return registry.CapacityInteractiveUsage
	}
	return registry.CapacityAPICredit
}

// laneStateFor reports what is actually known about the lane.
func laneStateFor(rec intake.ProviderRecord) registry.LaneState {
	if rec.VerifySkipped {
		return registry.LaneStateUnknown
	}
	return registry.LaneStateAvailable
}

// upsertProviderLane writes the provider's lane.
//
// ModelFilter is deliberately empty: "all known models". A filter copied
// from KnownModels at add time would freeze the lane to the model list one
// probe happened to see, and a later re-verify that discovers a new model
// would leave it unroutable with nothing reporting why.
func upsertProviderLane(ctx context.Context, reg *registry.Registry, rec intake.ProviderRecord) error {
	return reg.UpsertLane(ctx, registry.LaneRecord{
		LaneName:       laneNameFor(rec),
		ProviderName:   rec.Name,
		Weight:         1,
		PoolMembership: rec.Pool,
		PoolIndex:      rec.PoolIndex,
		Capacity:       laneCapacityFor(rec),
		State:          laneStateFor(rec),
	})
}
