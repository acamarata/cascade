// Purpose: the dry-run gate's audit trace — the row that records what the
//
//	simulation predicted, so an operator can see WHY a first auto-advance
//	was allowed to proceed rather than only that it was.
//
// WHY THE ROW CARRIES NO COMMAND TEXT. The append-only audit log is
//
//	deliberately not a second copy of what an action ran; ParamsHash binds
//	the row to the action the way the router's own row does. Everything
//	recorded here is the ENGINE's report about the action, in the engine's
//	own vocabulary.
//
// Constraints: a failed append is ignored. The guard's answer is already
//
//	computed when the trace is written, and replacing a decision with a
//	logging error would trade a working gate for a tidier log.
//
// SPORT: fleet.supervision.EnforceDryRunFirst/ADDED (P1-E18-W4-S39-T4).
//
//	Split out of dryrun_enforce.go under Art.10.3's 300-line cap.

package supervision

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
)

// emitTrace appends the dry run's evaluation trace to the audit log.
//
// The row carries the engine's own report — verdict, rung, deciding layer,
// explanation — and never the command text or parameters; ParamsHash binds
// it to the action the way the router's own row does. A failed append is
// ignored: the trace is supplementary, and the guard's answer (already
// computed) must not be replaced by a logging error.
func (g *DryRunFirstGuard) emitTrace(
	ctx context.Context, req policy.EvalRequest, account, version string,
	res policy.DryRunResult, simErr error, outcome string,
) {
	if g.trace == nil {
		return
	}
	explain, err := json.Marshal(dryRunTrace{
		Account: account, ProfileVersion: version, Outcome: outcome,
		Verdict: res.Verdict.String(), RiskLevel: res.RiskLevel.String(),
		MatchedRule: res.MatchedRule, Explanation: res.Explanation,
		ElevationRequired: res.ElevationRequired, AutoAdvance: res.AutoAdvance,
		EffectiveScope: string(res.EffectiveScope), WouldEmitAudit: res.WouldEmitAudit,
		Error: errText(simErr),
	})
	if err != nil {
		return
	}
	_, _ = g.trace.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      dryRunActor,
		Action:     actionRef(req),
		ParamsHash: audit.HashParams(req.Params),
		RiskLevel:  res.RiskLevel.String(),
		Verdict:    res.Verdict.String(),
		Outcome:    outcome,
		Explain:    explain,
	})
}

// dryRunTrace is the trace row's rationale payload. Every field answers
// "what would have happened" in the engine's own words; none reproduces
// anything the action carried.
type dryRunTrace struct {
	Account           string `json:"account"`
	ProfileVersion    string `json:"profile_version"`
	Outcome           string `json:"outcome"`
	Verdict           string `json:"verdict"`
	RiskLevel         string `json:"risk_level"`
	MatchedRule       string `json:"matched_rule"`
	Explanation       string `json:"explanation"`
	ElevationRequired bool   `json:"elevation_required"`
	AutoAdvance       bool   `json:"auto_advance"`
	EffectiveScope    string `json:"effective_scope"`
	WouldEmitAudit    bool   `json:"would_emit_audit"`
	Error             string `json:"error,omitempty"`
}

// errText flattens an error for a JSON payload.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
