package daemon

// Purpose: the "conductor.execute" wire params shape and its translation
//   into pkg/provider.ModelRequest, split out of conductor_execute.go to
//   stay under Art.10.3's 300-line cap. Mirrors cmd/cascade/run.go:169-188's
//   runRequestParams field-for-field (task_id/task_class/inputs/
//   requirements/sensitivity/fan_out/request_id) - that struct cannot be imported
//   here (cmd/cascade is package main), so its wire shape is duplicated,
//   not its Go type.
// Inputs: the raw JSON-RPC params for "conductor.execute".
// Outputs: a provider.ModelRequest, or a KindInvalidInput error for an
//   unparseable sensitivity name.
// Constraints: sensitivity arrives as the tier's String() name (never the
//   raw numeric encoding) per run.go's own CONTRACT DEVIATION note;
//   parseSensitivityTier delegates to provider.ParseSensitivityTier, the
//   one closed parser, and refuses on its error - no local name table.
// SPORT: internal/daemon (ADD, R-16.80).

import (
	"github.com/acamarata/cascade/internal/conductor"
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
	// RequestID is the client's optional fan-out id (fan_out >= 2 only):
	// a repeat call with the same id re-attaches to that fan-out.
	RequestID string `json:"request_id,omitempty"`
}

// validateFanOut checks a fan-out call's bound and request_id before any
// authorization or write: 2 <= fan_out <= conductor.MaxFanOut, and a
// request_id, when present, in the exact cascade.ParseID form.
func (p conductorExecuteParams) validateFanOut() error {
	if p.FanOut > conductor.MaxFanOut {
		return cascade.Newf(cascade.KindInvalidInput, "conductor.execute: fan_out %d exceeds the maximum of %d", p.FanOut, conductor.MaxFanOut)
	}
	if p.RequestID == "" {
		return nil
	}
	if _, err := cascade.ParseID(p.RequestID); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "conductor.execute: request_id")
	}
	return nil
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

// parseSensitivityTier parses name (a SensitivityTier.String() value)
// through provider.ParseSensitivityTier. An empty name is the documented
// zero value, SensitivityRestricted. An unrecognised name is refused with
// KindInvalidInput naming it; no ModelRequest is built from it, and the
// tier returned beside the error is the parser's local-only, so even a
// caller that dropped the error would hold the narrowest tier.
func parseSensitivityTier(name string) (provider.SensitivityTier, error) {
	tier, err := provider.ParseSensitivityTier(name)
	if err != nil {
		return tier, cascade.Wrap(cascade.KindInvalidInput, err, "conductor.execute: sensitivity")
	}
	return tier, nil
}
