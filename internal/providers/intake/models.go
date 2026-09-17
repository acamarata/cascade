package intake

// Purpose: decode a shape probe's 200 into the model list each vendor's
//   /models response carries (P1-E16-W4-S35-T12 split, for the 300-line
//   cap).
// Inputs: the driver kind the probe settled on, and the raw body.
// Outputs: the model IDs in the order the vendor listed them.
// Constraints: a 200 this build cannot decode is a fail-closed refusal,
//   never a partially-populated model list -- an empty list is itself a
//   micro-verify failure, so a silent one would register a provider
//   nothing had verified.
// SPORT: internal/providers/intake model enumeration (ADD) --
//   P1-E16-W4-S35-T12.

import (
	"encoding/json"
	"net/http"
)

// modelsFromProbe decodes body against kind's known models-list shape and
// returns the model IDs, in the order the vendor listed them. An unknown
// shape (a 200 this build cannot decode) is the fail-closed refusal
// errUnknownEndpointShape - never a partially-populated model list.
func modelsFromProbe(kind DriverKind, body []byte) ([]string, error) {
	switch kind {
	case DriverAnthropic:
		var wire struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, errUnknownEndpointShape(kind, http.StatusOK)
		}
		out := make([]string, 0, len(wire.Data))
		for _, m := range wire.Data {
			out = append(out, m.ID)
		}
		return out, nil
	case DriverOpenAICompat:
		var wire struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, errUnknownEndpointShape(kind, http.StatusOK)
		}
		out := make([]string, 0, len(wire.Data))
		for _, m := range wire.Data {
			out = append(out, m.ID)
		}
		return out, nil
	case DriverGemini:
		var wire struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			return nil, errUnknownEndpointShape(kind, http.StatusOK)
		}
		out := make([]string, 0, len(wire.Models))
		for _, m := range wire.Models {
			out = append(out, m.Name)
		}
		return out, nil
	case DriverOllama, DriverLocalLLM:
		// Local drivers are never shape-probed (there is no vendor
		// credential to try): they are selected only by an explicit
		// directive, and their own driver package owns model enumeration.
		return nil, errUnknownEndpointShape(kind, http.StatusOK)
	default:
		return nil, errUnknownEndpointShape(kind, http.StatusOK)
	}
}
