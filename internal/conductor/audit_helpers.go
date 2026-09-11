// Purpose: dispatch-boundary and audit helpers split out of execute.go
//   (P1-E11-W3-S23-T3) purely to stay under Art.10.3's 300-line/file cap
//   once the streaming/one-shot terminal-event work landed there - a
//   behavior-preserving split, same package, no signature changes (the
//   same pattern envelope.go/output.go and daemon_unix.go/
//   daemon_unix_run.go already use in this tree). Not listed in this
//   ticket's files_scope.add; see the journal for why the repo-wide
//   file-length gate took priority over the literal scope list.
// Inputs: a provider.ModelRequest/ChatResponse boundary projection; an
//   audit outcome tag and cause.
// Outputs: a provider.ChatRequest; a taxonomy-mapped error; one audit
//   record per call.
// Constraints: append is the single audit call site every terminal
//   outcome routes through (R-14.37) - unchanged from execute.go's own
//   original invariant.
// SPORT: conductor.execute/CHANGE (P1-E11-W3-S23-T3, file split only).

package conductor

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// toChatRequest projects a ModelRequest onto the ModelProvider driver
// boundary's ChatRequest shape.
func toChatRequest(req provider.ModelRequest) provider.ChatRequest {
	return provider.ChatRequest{
		Messages: req.Inputs,
		RequiredCapabilities: provider.RequiredCapabilities{
			Search:           contains(req.RequiredCapabilities, "search"),
			URLFetch:         contains(req.RequiredCapabilities, "url_fetch"),
			Vision:           contains(req.RequiredCapabilities, "vision"),
			ToolUse:          contains(req.RequiredCapabilities, "tool_use"),
			LongContext:      contains(req.RequiredCapabilities, "long_context"),
			StructuredOutput: contains(req.RequiredCapabilities, "structured_output"),
		},
	}
}

func contains(caps provider.RequiredCapabilities, name string) bool {
	switch name {
	case "search":
		return caps.Search
	case "url_fetch":
		return caps.URLFetch
	case "vision":
		return caps.Vision
	case "tool_use":
		return caps.ToolUse
	case "long_context":
		return caps.LongContext
	case "structured_output":
		return caps.StructuredOutput
	}
	return false
}

// mapProviderError maps a provider-side error onto the A-T7 taxonomy. An
// error already carrying a taxonomy Kind passes through unchanged; anything
// else is wrapped KindInternal rather than surfaced raw.
func mapProviderError(err error) error {
	if _, ok := cascade.KindOf(err); ok {
		return err
	}
	return cascade.Wrap(cascade.KindInternal, err, "conductor: provider dispatch failed")
}

// auditRefusal writes the pre-dispatch-rejection audit record.
func (e *Executor) auditRefusal(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier, cause error) {
	e.append(ctx, req, tier, provider.Selection{}, "refusal", cause)
}

// auditOutcome writes the terminal audit record for a dispatched request.
func (e *Executor) auditOutcome(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier, sel provider.Selection, outcome string, cause error) {
	e.append(ctx, req, tier, sel, outcome, cause)
}

// append is the single audit call site every terminal outcome routes
// through, so Execute and ExecuteStream never write more or fewer than one
// record per call. The Explain payload carries only the redacted trace and
// cost accounting - never a raw request field.
func (e *Executor) append(ctx context.Context, req provider.ModelRequest, tier provider.SensitivityTier, sel provider.Selection, outcome string, cause error) {
	trace := ExecutionTrace{LaneID: sel.LaneID, Outcome: outcome}
	if cause != nil {
		trace.Reason = cause.Error()
	}
	explain, _ := json.Marshal(trace)
	paramsHash := audit.HashParams([]byte(req.TaskID + "|" + req.TaskClass))
	_, _ = e.pipeline.cfg.Audit.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      "conductor",
		Action:     "model.execute",
		ParamsHash: paramsHash,
		RiskLevel:  tier.String(),
		Verdict:    outcome,
		Outcome:    outcome,
		Explain:    explain,
	})
}
