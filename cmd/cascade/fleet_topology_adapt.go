package main

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fleetTopologyStore pairs the topology rows with their observation cache.
type fleetTopologyStore struct {
	*topology.Store
	Quota *topology.QuotaStore
}

type fleetTopologyCeiling func(context.Context, topology.LaneID, bool) (topology.Ceiling, error)

type fleetTopologyAdapter struct {
	store   fleetTopologyStore
	ceiling fleetTopologyCeiling
}

// fleetTopologyDeps projects stored rows onto public DTOs. The caller supplies
// the live CeilingForLane method and the process detector. Every lane read
// recomputes its ceiling with no restricted-data permit.
func fleetTopologyDeps(store fleetTopologyStore, ceiling fleetTopologyCeiling, detector *secrets.Detector) rpc.FleetTopologyDeps {
	a := fleetTopologyAdapter{store: store, ceiling: ceiling}
	return rpc.FleetTopologyDeps{
		Lanes: a.lanes, Lane: a.lane, Domains: a.domains, Domain: a.domain,
		Scan: func(payload []byte) error {
			if detector == nil {
				return cascade.New(cascade.KindUnavailable, "fleet topology: detector unavailable")
			}
			if len(detector.Scan(payload)) != 0 {
				return cascade.New(cascade.KindIntegrity, "fleet topology: response withheld by the credential detector")
			}
			return nil
		},
	}
}

func (a fleetTopologyAdapter) ready() error {
	if a.store.Store == nil || a.store.Quota == nil {
		return cascade.New(cascade.KindUnavailable, "fleet topology: store unavailable")
	}
	return nil
}

func (a fleetTopologyAdapter) laneDTO(ctx context.Context, l topology.Lane) rpc.LaneDTO {
	d := rpc.LaneDTO{
		ID: string(l.ID), RuntimeProfile: string(l.RuntimeProfileRef), QuotaDomain: string(l.QuotaDomainRef),
		ModelID: l.ModelID, Effort: string(l.Effort), LaneClass: string(l.LaneClass),
		InteractionClass: string(l.InteractionClass), Health: string(l.Health),
		HealthReason: "stored lane health: " + string(l.Health), BaseShadowPrice: l.BaseShadowPrice,
		Retired: l.RetiredAt != nil, DiscoveredAt: l.DiscoveredAt,
		Ceiling: "unavailable", CeilingReason: cascade.KindUnavailable.String(),
	}
	if a.ceiling == nil {
		return d
	}
	c, err := a.ceiling(ctx, l.ID, false)
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok {
			d.CeilingReason = kind.String()
		}
		return d
	}
	if !c.Tier.Valid() || !c.Reason.Valid() {
		d.CeilingReason = cascade.KindInvalidInput.String()
		return d
	}
	d.Ceiling, d.CeilingReason = c.Tier.String(), string(c.Reason)
	return d
}

func (a fleetTopologyAdapter) lanes(ctx context.Context, retired bool) ([]rpc.LaneDTO, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	rows, err := a.store.ListLanes(ctx, retired)
	if err != nil {
		return nil, err
	}
	out := make([]rpc.LaneDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, a.laneDTO(ctx, row))
	}
	return out, nil
}

func (a fleetTopologyAdapter) lane(ctx context.Context, id string) (rpc.LaneExplainDTO, error) {
	if err := a.ready(); err != nil {
		return rpc.LaneExplainDTO{}, err
	}
	l, err := a.store.GetLane(ctx, topology.LaneID(id))
	if err != nil {
		return rpc.LaneExplainDTO{}, err
	}
	p, err := a.store.GetRuntimeProfile(ctx, l.RuntimeProfileRef)
	if err != nil {
		return rpc.LaneExplainDTO{}, err
	}
	d, err := a.domain(ctx, string(l.QuotaDomainRef))
	if err != nil {
		return rpc.LaneExplainDTO{}, err
	}
	return rpc.LaneExplainDTO{
		Lane: a.laneDTO(ctx, l), Domain: d,
		Profile: rpc.ProfileDTO{ID: string(p.ID), Runtime: string(p.Runtime), Persistent: p.Persistent, Local: topology.EndpointIsLocal(p)},
	}, nil
}

func (a fleetTopologyAdapter) domains(ctx context.Context) ([]rpc.DomainDTO, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	rows, err := a.store.ListQuotaDomains(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]rpc.DomainDTO, 0, len(rows))
	for _, row := range rows {
		d, err := a.domainDTO(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (a fleetTopologyAdapter) domain(ctx context.Context, id string) (rpc.DomainDTO, error) {
	if err := a.ready(); err != nil {
		return rpc.DomainDTO{}, err
	}
	d, err := a.store.GetQuotaDomain(ctx, topology.DomainID(id))
	if err != nil {
		return rpc.DomainDTO{}, err
	}
	return a.domainDTO(ctx, d)
}

func (a fleetTopologyAdapter) domainDTO(ctx context.Context, d topology.QuotaDomain) (rpc.DomainDTO, error) {
	rows, err := a.store.Quota.ListBuckets(ctx, d.ID)
	if err != nil {
		return rpc.DomainDTO{}, err
	}
	holders, err := a.store.ListCredentials(ctx)
	if err != nil {
		return rpc.DomainDTO{}, err
	}
	out := rpc.DomainDTO{ID: string(d.ID), Kind: string(d.Kind), BillingTier: string(d.BillingTier), Quarantined: d.Quarantined, Buckets: make([]rpc.BucketDTO, 0, len(rows))}
	for _, holder := range holders {
		if holder.QuotaDomainRef == d.ID {
			out.Holders++
		}
	}
	for _, b := range rows {
		out.Buckets = append(out.Buckets, rpc.BucketDTO{
			Name: b.Name, Window: string(b.Window), Source: string(b.Source), Limit: b.Limit,
			RemainingFraction: b.RemainingFraction, Confidence: b.Confidence, ResetAt: b.ResetAt, ObservedAt: b.ObservedAt,
		})
	}
	return out, nil
}
