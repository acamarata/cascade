package tools

// Purpose (this file): cascade_cpa_send — record one conversation turn.
// SPORT: plugins/cascade-pa:mcp-tools:cascade_cpa_send (ADD) — P1-E20-W5-S43-T4.

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The four sensitivity tiers a caller may name.
const (
	SensitivityLocalOnly  = "local-only"
	SensitivityRestricted = "restricted"
	SensitivityInternal   = "internal"
	SensitivityPublic     = "public"
)

// SendInput is cascade_cpa_send's parameter schema.
type SendInput struct {
	// Content is the turn's text (required).
	Content string `json:"content"`
	// ThreadID continues a thread. Omitted, a thread is started and the
	// result names it.
	ThreadID string `json:"thread_id,omitempty"`
	// Sensitivity is the tier this content carries. Anything unset or
	// unrecognised resolves to restricted — see ResolveSensitivity.
	Sensitivity string `json:"sensitivity,omitempty"`
}

// SendOutput is cascade_cpa_send's result.
type SendOutput struct {
	TurnID    string `json:"turn_id"`
	ThreadID  string `json:"thread_id"`
	CreatedAt string `json:"created_at"`
	// Sensitivity is the tier this turn was recorded under — ECHOED, not
	// assumed. A caller that misspelled a tier gets "restricted" back and
	// can see that its request was narrowed, rather than believing its
	// public turn is public (06 §5.16).
	Sensitivity string `json:"sensitivity"`
}

// ResolveSensitivity maps a caller's tier to the one actually used.
//
// FAIL CLOSED (06 §5.16): unset, unknown, misspelled and
// differently-cased all resolve to restricted. There is exactly one
// direction an unrecognised value may resolve in, and it is the narrow
// one — a typo that widened a tier would be silent until the content had
// already left.
func ResolveSensitivity(v string) string {
	switch v {
	case SensitivityLocalOnly, SensitivityRestricted, SensitivityInternal, SensitivityPublic:
		return v
	default:
		return SensitivityRestricted
	}
}

// send records the turn.
func (d *Dispatcher) send(ctx context.Context, input []byte) ([]byte, error) {
	in, err := decodeToolInput[SendInput](ToolSend, input)
	if err != nil {
		return nil, err
	}
	if in.Content == "" {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"%s: content is empty; there is nothing to record", ToolSend)
	}
	res, err := d.svc.Append(ctx, AppendRequest{
		ThreadID: in.ThreadID,
		Role:     "user",
		Content:  in.Content,
	})
	if err != nil {
		return nil, err
	}
	// The SERVER's thread id, never the one the caller sent: an
	// implementation that echoed the request's empty string back would
	// leave an agent unable to continue the thread it just started
	// (R-14.285).
	if res.ThreadID == "" {
		return nil, cascade.Newf(cascade.KindInternal,
			"%s: the conversation service recorded a turn and named no thread", ToolSend)
	}
	return json.Marshal(SendOutput{
		TurnID:      res.TurnID,
		ThreadID:    res.ThreadID,
		CreatedAt:   res.CreatedAt,
		Sensitivity: ResolveSensitivity(in.Sensitivity),
	})
}

// decodeToolInput decodes one tool's parameters, refusing unknown fields.
//
// Unknown fields are an ERROR rather than ignored: a model that sent
// {"contnet": "..."} asked to record something, and silently recording an
// empty turn — or, worse, silently ignoring a `thread_id` typo and
// starting a new thread every time — is the failure an agent cannot see.
// Empty input decodes to the zero value, which each tool then validates on
// its own terms.
func decodeToolInput[T any](tool string, raw []byte) (T, error) {
	var in T
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return in, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, cascade.Wrapf(cascade.KindInvalidInput, err, "%s: decoding parameters", tool)
	}
	return in, nil
}
