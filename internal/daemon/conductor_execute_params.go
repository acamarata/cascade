package daemon

// Purpose: the "conductor.execute" wire params shape and its translation
//   into pkg/provider.ModelRequest, split out of conductor_execute.go to
//   stay under Art.10.3's 300-line cap. Mirrors cmd/cascade/run.go:169-188's
//   runRequestParams field-for-field (task_id/task_class/inputs/
//   requirements/sensitivity/fan_out) - that struct cannot be imported
//   here (cmd/cascade is package main), so its wire shape is duplicated,
//   not its Go type.
// Inputs: the raw JSON-RPC params for "conductor.execute".
// Outputs: a provider.ModelRequest, or a KindInvalidInput error for an
//   unparseable sensitivity name.
// Constraints: sensitivity arrives as the tier's String() name (never the
//   raw numeric encoding) per run.go's own CONTRACT DEVIATION note;
//   parseSensitivityTier is the sole parser, built from provider's own
//   exported tier constants rather than a second, driftable name table.
// SPORT: internal/daemon (ADD, R-16.80).

import (
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// conductorExecuteParams is "conductor.execute"'s decoded request params.
type conductorExecuteParams struct {
	TaskID       string                    `json:"task_id"`
	TaskClass    string                    `json:"task_class"`
	Inputs       []conductorExecuteMessage `json:"inputs"`
	Requirements provider.Requirements     `json:"requirements"`
	Sensitivity  string                    `json:"sensitivity"`
	FanOut       int                       `json:"fan_out,omitempty"`
}

// conductorExecuteMessage mirrors cmd/cascade/run.go's runChatMessage.
type conductorExecuteMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// toModelRequest translates p into the SDK's ModelRequest shape.
func (p conductorExecuteParams) toModelRequest() (provider.ModelRequest, error) {
	tier, err := parseSensitivityTier(p.Sensitivity)
	if err != nil {
		return provider.ModelRequest{}, err
	}
	inputs := make([]provider.ChatMessage, len(p.Inputs))
	for i, m := range p.Inputs {
		inputs[i] = provider.ChatMessage{Role: m.Role, Content: m.Content}
	}
	return provider.ModelRequest{
		TaskID:       p.TaskID,
		TaskClass:    p.TaskClass,
		Inputs:       inputs,
		Requirements: p.Requirements,
		Sensitivity:  tier,
		FanOut:       p.FanOut,
	}, nil
}

// sensitivityTiers lists every declared provider.SensitivityTier member,
// used to parse a wire name back into its tier without a second,
// driftable string table (provider.SensitivityTier.String() is the only
// source of truth this file reads).
var sensitivityTiers = []provider.SensitivityTier{
	provider.SensitivityRestricted,
	provider.SensitivityLocalOnly,
	provider.SensitivityInternal,
	provider.SensitivityPublic,
}

// parseSensitivityTier parses name (a SensitivityTier.String() value) back
// into its tier. An empty name resolves to SensitivityRestricted, matching
// ModelRequest's own documented fail-closed zero value. An unrecognized
// name is a KindInvalidInput error, never a silent fallback.
func parseSensitivityTier(name string) (provider.SensitivityTier, error) {
	if name == "" {
		return provider.SensitivityRestricted, nil
	}
	for _, t := range sensitivityTiers {
		if t.String() == name {
			return t, nil
		}
	}
	return 0, cascade.Newf(cascade.KindInvalidInput, "conductor.execute: unknown sensitivity %q", name)
}
