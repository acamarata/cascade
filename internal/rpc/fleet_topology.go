package rpc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// topologyEnvelope is the versioned result shape of every fleet topology method.
type topologyEnvelope struct {
	Version string `json:"version"`
	Data    any    `json:"data"`
}

type topologyIDParams struct {
	ID string `json:"id"`
}

type topologyListParams struct {
	IncludeRetired bool `json:"include_retired"`
}

// RegisterFleetTopology registers the five owner-only fleet.lanes and
// fleet.domains methods. A nil reg or any nil dep is KindInvalidInput and
// registers nothing.
func RegisterFleetTopology(reg *Registry, d FleetTopologyDeps) error {
	if reg == nil || d.Lanes == nil || d.Lane == nil || d.Domains == nil || d.Domain == nil || d.Scan == nil {
		return cascade.New(cascade.KindInvalidInput, "fleet topology: registry and all five deps are required")
	}
	reg.Register("fleet.lanes.list", topologyHandler(d.Scan, "fleet.lanes.list",
		func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p topologyListParams
			if err := decodeTopologyParams(raw, &p); err != nil {
				return nil, err
			}
			v, err := d.Lanes(ctx, p.IncludeRetired)
			if v == nil {
				v = []LaneDTO{}
			}
			return v, err
		}))
	reg.Register("fleet.lanes.show", topologyHandler(d.Scan, "fleet.lanes.show",
		topologyByID(func(ctx context.Context, id string) (any, error) {
			v, err := d.Lane(ctx, id)
			return v.Lane, err
		})))
	reg.Register("fleet.lanes.explain", topologyHandler(d.Scan, "fleet.lanes.explain",
		topologyByID(func(ctx context.Context, id string) (any, error) { return d.Lane(ctx, id) })))
	reg.Register("fleet.domains.list", topologyHandler(d.Scan, "fleet.domains.list",
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			v, err := d.Domains(ctx)
			if v == nil {
				v = []DomainDTO{}
			}
			return v, err
		}))
	reg.Register("fleet.domains.show", topologyHandler(d.Scan, "fleet.domains.show",
		topologyByID(func(ctx context.Context, id string) (any, error) { return d.Domain(ctx, id) })))
	return nil
}

// decodeTopologyParams decodes optional params; empty or null params leave v zero.
func decodeTopologyParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return topologyParamError("fleet topology: malformed params")
	}
	return nil
}

// topologyByID adapts an id lookup into a params-decoding fetch that rejects an empty id.
func topologyByID(f func(ctx context.Context, id string) (any, error)) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p topologyIDParams
		if err := decodeTopologyParams(raw, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, topologyParamError("fleet topology: id is required")
		}
		return f(ctx, p.ID)
	}
}

// topologyHandler runs fetch, wraps the result in the versioned envelope,
// passes the marshaled bytes through scan, and returns exactly the scanned
// bytes. Dependency errors keep their Kind and lose their message.
func topologyHandler(scan func([]byte) error, method string, fetch func(context.Context, json.RawMessage) (any, error)) HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		data, err := fetch(ctx, params)
		if err != nil {
			var pe topologyParamError
			if errors.As(err, &pe) {
				return nil, cascade.New(cascade.KindInvalidInput, string(pe))
			}
			kind, ok := cascade.KindOf(err)
			if !ok {
				kind = cascade.KindUnavailable
			}
			return nil, cascade.New(kind, "fleet topology: "+method+" failed")
		}
		out, err := json.Marshal(topologyEnvelope{Version: FleetTopologyEnvelopeVersion, Data: data})
		if err != nil {
			return nil, cascade.New(cascade.KindInternal, "fleet topology: "+method+" failed")
		}
		if scan(out) != nil {
			return nil, cascade.New(cascade.KindIntegrity, "fleet topology: response withheld by the credential detector")
		}
		return json.RawMessage(out), nil
	}
}

// topologyParamError marks a handler-local parameter rejection, safe to return as KindInvalidInput.
type topologyParamError string

func (e topologyParamError) Error() string { return string(e) }
