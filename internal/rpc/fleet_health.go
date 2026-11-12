package rpc

// Purpose: expose a credential-free health summary supplied by the composition root.
// Inputs: an injected summary function and empty RPC params. Outputs: the closed DTO.
// Constraints: validate without computing health; source errors return no body.
// SPORT: fleet.health.summary.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FleetHealthSummaryMethod names the fleet health summary RPC.
const FleetHealthSummaryMethod = "fleet.health.summary"

// FleetHealthKeyDTO carries one credential-free health reading.
type FleetHealthKeyDTO struct {
	Key        string   `json:"key"`
	State      string   `json:"state"`
	Percentage *float64 `json:"percentage,omitempty"`
}

// FleetHealthSummaryDTO carries the five ordered health readings and RPC status.
type FleetHealthSummaryDTO struct {
	Status string               `json:"status"`
	Keys   [5]FleetHealthKeyDTO `json:"keys"`
}

// FleetHealthDeps supplies the summary through a composition-root adapter.
type FleetHealthDeps struct {
	Summary func(ctx context.Context) (FleetHealthSummaryDTO, error)
}

var fleetHealthKeyOrder = [5]string{"executive", "build", "bulk", "adversary", "long_context"}
var fleetHealthStates = [6]string{"healthy", "protected", "scarce", "abundant", "available", "unavailable"}

// RegisterFleetHealthSummary registers a handler after validating its dependencies.
func RegisterFleetHealthSummary(registry *Registry, deps FleetHealthDeps) error {
	if registry == nil || deps.Summary == nil {
		return cascade.New(cascade.KindInvalidInput, "fleet.health.summary: registry and summary required")
	}
	registry.Register(FleetHealthSummaryMethod, func(ctx context.Context, params json.RawMessage) (any, error) {
		if err := decodeFleetHealthParams(params); err != nil {
			return nil, err
		}
		summary, err := deps.Summary(ctx)
		if err != nil {
			return nil, cascade.New(cascade.KindUnavailable, "fleet.health.summary: health source unavailable")
		}
		if !validFleetHealthSummary(summary) {
			return nil, cascade.New(cascade.KindInternal, "fleet.health.summary: malformed health source")
		}
		// Construct the public response explicitly; source status is untrusted.
		return FleetHealthSummaryDTO{Status: "ok", Keys: summary.Keys}, nil
	})
	return nil
}

func decodeFleetHealthParams(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	invalid := cascade.New(cascade.KindInvalidInput, "fleet.health.summary: invalid params")
	if raw[0] != '{' {
		return invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var params struct{}
	if err := decoder.Decode(&params); err != nil {
		return invalid
	}
	if err := decoder.Decode(&params); err != io.EOF {
		return invalid
	}
	return nil
}

func validFleetHealthSummary(summary FleetHealthSummaryDTO) bool {
	for i, entry := range summary.Keys {
		if entry.Key != fleetHealthKeyOrder[i] {
			return false
		}
		valid := false
		for _, state := range fleetHealthStates {
			if entry.State == state {
				valid = true
				break
			}
		}
		if !valid {
			return false
		}
	}
	return true
}
