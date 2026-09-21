// Purpose: Provider, the native adversarial reviewer's
//   pkg/provider.ReviewProvider implementation (P1-E25-W5-S52-T4): the
//   frozen ABI entry point (Review, Capabilities) an external caller (e.g.
//   cascade-pbd, per the ticket's Art.2 real-counterpart test) reaches.
//   Every dispatch goes through the injected provider.ModelExecutor --
//   P1-EXEC-20260918's resolved alias for the ticket's stale "conductor.
//   Execute"/"Task{}" prose: the real door is provider.ModelExecutor.
//   Execute(ctx, ModelRequest)(ModelResponse, error), which
//   internal/conductor.Executor implements daemon-side and
//   pkg/provider.Client.ModelExecute reaches over the daemon's real
//   "conductor.execute" JSON-RPC door for an out-of-daemon caller (the
//   SAME seam cmd/cascade/run_exec.go's fetchRun dials) -- never a direct
//   provider call.
// Inputs: a provider.ModelExecutor and a provider.ProviderRegistryReader,
//   injected by the caller (production: internal/plugins/review_wiring.go).
// Outputs: provider.ReviewResponse per dispatch, or a typed error.
// Constraints: this file decides NO sensitivity policy of its own (CR fix
//   D1). ModelRequest.Sensitivity is the level template's tier, which is the
//   §5.16 task-class default and never below it, and the CALLER's ctx is
//   passed to Execute unchanged so internal/conductor's FILTER 0 and FILTER
//   2 apply the thread's real privacy mode -- the local-only refusal is the
//   ROUTER's, proven against the real router in router_test.go. Every
//   dispatch's model output is parsed into the returned Findings -- no gate
//   that fires-and-forgets a model call -- and every R-21.191 exclusion is
//   surfaced to the caller as a note finding rather than dropped silently.
// SPORT: internal/review.provider/ADD (P1-E25-W5-S52-T4).

// Package review implements the native adversarial code reviewer
// (P1-E25-W5-S52-T4): the CR-A/CR-B/CR-C conductor task templates
// (templates.go), the R-21.156/R-21.191 blind request construction
// (request.go), the pkg/provider.ReviewProvider implementation (this
// file), and the CR-A/CR-B/CR-C dispatch orchestration including the CR-C
// two-pass adversarial fan-out (reviewer.go). plugins/review is this
// engine's plugin skin only -- see that package's own doc comment.
package review

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Provider implements pkg/provider.ReviewProvider. Build with NewProvider;
// the zero value is not usable.
type Provider struct {
	executor provider.ModelExecutor
	registry provider.ProviderRegistryReader
	events   EventPublisher
}

// NewProvider constructs a Provider from its collaborators. The executor and
// the registry are required -- a nil one is refused at construction, never
// discovered later as a nil-pointer panic mid-dispatch. events MAY be nil
// (events.go: a nil publisher drops every event), which is what a test that
// does not care about telemetry passes; production wires a real one in
// internal/plugins/review_wiring.go.
func NewProvider(exec provider.ModelExecutor, registry provider.ProviderRegistryReader, events EventPublisher) (*Provider, error) {
	if exec == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "internal/review: NewProvider requires a non-nil ModelExecutor")
	}
	if registry == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "internal/review: NewProvider requires a non-nil ProviderRegistryReader")
	}
	return &Provider{executor: exec, registry: registry, events: events}, nil
}

// compile-time proof Provider satisfies the ABI Art.2's real-counterpart
// test dispatches through.
var _ provider.ReviewProvider = (*Provider)(nil)

// Review implements provider.ReviewProvider.Review: the ABI-frozen entry
// point. It builds the dispatch plan (which is where the R-21.191 filter and
// the D7 fail-closed artifact-format check run) and delegates to this
// package's own Review orchestration function (reviewer.go), which the
// CR-A/CR-B/CR-C split, the CR-C two-pass fan-out and the family gate all
// live behind. ctx reaches Execute UNCHANGED: the thread privacy a caller
// attached is what the router enforces, and swapping in a fresh context here
// would silently strip it.
func (p *Provider) Review(ctx context.Context, req provider.ReviewRequest) (provider.ReviewResponse, error) {
	if !req.Level.Valid() {
		return provider.ReviewResponse{}, cascade.Newf(cascade.KindInvalidInput, "internal/review: invalid review level %q", req.Level)
	}
	plan, err := NewPlan(req, ConsequenceClassForLevel(req.Level))
	if err != nil {
		return provider.ReviewResponse{}, err
	}
	resp, _, err := Review(ctx, plan, req.Level, p.executor, p.registry, p.events)
	return resp, err
}

// Capabilities implements provider.ReviewProvider.Capabilities. An empty
// lane reports this reviewer's own fixed structural requirement
// (StructuredOutput: every CR level sets Requirements.Structured=true); a
// named lane proxies to the real registry (J/S-19/J/S-20), reporting what
// that lane actually advertises rather than a fabricated value.
func (p *Provider) Capabilities(ctx context.Context, lane string) (provider.Capabilities, error) {
	if lane == "" {
		return provider.Capabilities{
			StructuredOutput:  provider.CapabilitySupported,
			CompliancePosture: provider.NewCompliancePosture(nil, false, false, nil, "", false),
		}, nil
	}
	info, err := p.registry.GetProvider(ctx, lane)
	if err != nil {
		return provider.Capabilities{}, err
	}
	return info.Capabilities, nil
}

// buildModelRequest turns a blind request and its level's template into
// the provider.ModelRequest a single dispatch sends. taskID distinguishes
// concurrent dispatches sharing one CheckpointID (CR-C's propose/challenge
// pair, reviewer.go). The prompt carries blind.Rubric -- NOT tmpl.Rubric() --
// because the blind request's Rubric is the one that already has the
// caller's verbatim Context and the AMD-20260916/6 checklist instructions
// appended to it (request.go's renderRubric). Sensitivity is the template's
// tier, which is the §5.16 class default: this function never lowers it.
func buildModelRequest(blind BlindRequest, tmpl TaskTemplate, taskID string) provider.ModelRequest {
	return provider.ModelRequest{
		TaskID:       taskID,
		TaskClass:    string(tmpl.TaskClass()),
		Inputs:       []provider.ChatMessage{{Role: "user", Content: blind.Rubric + "\n\nARTIFACT:\n" + blind.Artifact}},
		Requirements: tmpl.Requirements(),
		Sensitivity:  tmpl.Sensitivity(),
	}
}

// wrapDispatch wraps a dispatch failure WITHOUT reclassifying it. An error
// that already carries a taxonomy Kind keeps it -- the router's own
// KindPolicyDenied sensitivity refusal must not arrive at the caller as
// "unavailable", which is what a blanket KindUnavailable wrap did and what
// router_test.go's kind assertion caught. Only an unclassified error (a bare
// errors.New from a double, or a driver that lost its kind) falls back to
// KindUnavailable, the taxonomy's kind for a dispatch that could not run.
func wrapDispatch(err error, msg string) error {
	if kind, ok := cascade.KindOf(err); ok {
		return cascade.Wrapf(kind, err, "%s", msg)
	}
	return cascade.Wrap(cascade.KindUnavailable, err, msg)
}

// wireFinding/wireResult mirror the structured JSON payload every review
// dispatch's Requirements.Structured=true instructs the model to emit.
type wireFinding struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

type wireResult struct {
	Approved  bool                  `json:"approved"`
	Findings  []wireFinding         `json:"findings"`
	Checklist []wireChecklistAnswer `json:"checklist"`
	Executed  []wireExecutedCheck   `json:"executed_checks"`
}

// parseFindings decodes a dispatch's structured Output into a
// ReviewResponse -- the model's output is always CONSUMED here, never
// discarded: a gate that dispatched and ignored the result is exactly what
// this function exists to prevent. A parse failure or an invalid severity
// is a real, typed error, never a silently-empty result passed off as "no
// findings".
func parseFindings(output string, checks Checklist) (provider.ReviewResponse, error) {
	var w wireResult
	if err := json.Unmarshal([]byte(output), &w); err != nil {
		return provider.ReviewResponse{}, cascade.Wrap(cascade.KindInternal, err, "internal/review: parse structured review output")
	}
	findings, err := decodeFindings(w.Findings)
	if err != nil {
		return provider.ReviewResponse{}, err
	}
	violations := checks.Violations(w.Checklist, w.Executed)
	findings = append(findings, violations...)
	approved := w.Approved && len(violations) == 0
	return provider.ReviewResponse{Findings: findings, Approved: approved}, nil
}

// decodeFindings converts the wire findings, refusing an invalid severity
// rather than passing a silently-empty result off as "no findings".
func decodeFindings(in []wireFinding) ([]provider.ReviewFinding, error) {
	out := make([]provider.ReviewFinding, 0, len(in))
	for _, f := range in {
		sev := provider.ReviewSeverity(f.Severity)
		if !sev.Valid() {
			return nil, cascade.Newf(cascade.KindInternal, "internal/review: model returned invalid severity %q", f.Severity)
		}
		out = append(out, provider.ReviewFinding{Severity: sev, File: f.File, Line: f.Line, Message: f.Message})
	}
	return out, nil
}

// exclusionNote renders the R-21.191 exclusion note the ABI can carry: a nit
// finding listing every excluded path. pkg/provider.ReviewResponse has only
// Findings and Approved (O/S-33.T1's frozen shape), so a finding is the one
// channel that reaches the caller -- an exclusion is never silent (D7).
func exclusionNote(excluded []string) (provider.ReviewFinding, bool) {
	if len(excluded) == 0 {
		return provider.ReviewFinding{}, false
	}
	return provider.ReviewFinding{
		Severity: provider.ReviewSeverityNit,
		Message: "internal/review: the following path(s) were excluded from the reviewer's context per R-21.191 " +
			"and were NOT reviewed: " + strings.Join(excluded, ", "),
	}, true
}

// dispatchOnce performs a single conductor.execute dispatch (CR-A/CR-B: no
// fan-out) and consumes its output into a ReviewResponse. Returns the
// router's Selection alongside the response so the caller (reviewer.go) can
// record which lane/family actually served the dispatch.
func dispatchOnce(ctx context.Context, exec provider.ModelExecutor, plan Plan, tmpl TaskTemplate) (provider.ReviewResponse, provider.Selection, error) {
	req := buildModelRequest(plan.Blind, tmpl, plan.Blind.CheckpointID)
	resp, err := exec.Execute(ctx, req)
	if err != nil {
		return provider.ReviewResponse{}, provider.Selection{}, wrapDispatch(err, "internal/review: conductor.execute dispatch failed")
	}
	out, err := parseFindings(resp.Output, plan.Checks)
	if err != nil {
		return provider.ReviewResponse{}, resp.Selection, err
	}
	if note, ok := exclusionNote(plan.Excluded); ok {
		out.Findings = append(out.Findings, note)
	}
	return out, resp.Selection, nil
}
