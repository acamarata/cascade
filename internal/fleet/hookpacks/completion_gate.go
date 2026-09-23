package hookpacks

// Purpose (this file): the AF/S-66.T1 completion-gate hook pack -- R-16.16's
//
//	binding that a harness completion (TaskCompleted/Stop) is EVIDENCE, not
//	the completion decision itself. handleCompletionHook is the fail-closed
//	bridge between a harness-native hook invocation and
//	policy.completion_check (internal/jobs.CompletionPolicy.Transition, via
//	the injected PolicyGate seam): timeout, a malformed job-scoped payload,
//	an unknown job id, or the gate's own denial all produce Deny with the
//	real reason surfaced verbatim -- never fail-open.
//
// FAIL-OPEN VS FAIL-CLOSED, ON PURPOSE (package-level note, Art.10.6). This
// package now carries hooks with OPPOSITE default directions and that is
// deliberate, not an inconsistency: the hydration/session hooks in
// handler.go are best-effort telemetry (R-16.6) -- a slow or absent daemon
// must never block an ordinary tool call, so those fail OPEN (dispatch
// swallows errors, the rendered command is `|| true`). The completion gate
// governs whether a job's own state may advance past a policy-reserved
// edge (R-16.12) -- an agent's own "I'm done" claim is not proof, so this
// hook fails CLOSED in every direction (R-16.16): timeout, malformed
// input, an unresolvable job id once the payload is job-scoped, and a
// real policy denial. R-21.176 narrows WHERE that fail-closed default
// applies (job-scoped completions only; see completion_scope.go's package
// note) -- it does not soften HOW it fails once scope is established.
//
// Inputs: a CompletionHookPayload (parsed from the harness's own hook
//
//	wire format via ParseCompletionHookPayload), an injected PolicyGate,
//	and an injected JobResolver.
//
// Outputs: CompletionHookResponse (handleCompletionHook), or a
//
//	registered "completion-gate" HookPack (RegisterCompletionHookPack)
//	plus a live fleet.sessions.completion_check RPC method
//	(RegisterCompletionCheckHandler) once wired at daemon startup
//	(cmd/cascade/hooks.go).
//
// Constraints: PolicyGate carries no concrete type here -- the real
//
//	adapter over *jobs.CompletionPolicy is cmd/cascade/hooks.go's
//	composition-root concern (internal/fleet/hookpacks may not import
//	internal/jobs: hookpacks sits below jobs in the same direction
//	internal/rpc does, per internal/rpc/jobs.go's own documented cycle).
//	Every Deny this file produces is journaled (recordDenial) with the
//	job id, hook event, reason and timestamp BEFORE the response is
//	returned -- a caller never blocks silently.
//
// SPORT: fleet/hookpacks.CompletionHookPayload/CompletionHookResponse/
//
//	PolicyGate/RegisterCompletionHookPack/ADD (P1-E32-W6-S66-T1).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodCompletionCheck is the fleet.sessions.completion_check JSON-RPC
// 2.0 method name: the daemon-side entry point a rendered "completion-gate"
// hook command's curl call posts to, and waits synchronously for (unlike
// the fire-and-forget MethodHookEvent in handler.go -- a completion check
// must be able to say no).
const MethodCompletionCheck = "fleet.sessions.completion_check"

// defaultCompletionTimeout is R-16.74's stated default for
// [fleet.hooks].completion_timeout. No [fleet.hooks] section exists in
// runtime.Config yet (verified against internal/runtime -- an Art.9 gap
// recorded in this ticket's journal, not invented here): this constant is
// the real, applied value until that section lands, and
// RegisterCompletionCheckHandler accepts an explicit override so a future
// config-reading composition root never needs to touch this file again.
const defaultCompletionTimeout = 10 * time.Second

// gateNamespace/EventGateDenied/EventGateTimeout mirror
// internal/jobs.completionGateNamespace/EventGateDenied and the new
// jobs.EventGateTimeout EXACTLY (string-for-string) rather than importing
// internal/jobs (a real, proven import cycle -- internal/rpc/jobs.go's
// own header; hookpacks sits on the same side of it). stall.go (the
// R-16.73 stall detector these feed) already carries this exact
// duplication for the identical reason -- see its gateDeniedNamespace/
// gateDeniedKind. CR-B Q1/D2 (binding): this file's OWN denials (unknown
// job id, nil gate, a non-timeout gate error, a real policy denial
// surfaced verbatim) previously journaled under a self-invented
// "hookpacks.completion" namespace the stall detector never subscribes
// to. They now publish EventGateDenied on gateNamespace, exactly as a
// jobs.CompletionPolicy.Transition denial does (completion.go's cp.deny).
// A timeout is not itself a policy denial the counting rule was designed
// for, so it publishes EventGateTimeout instead (R-16.74: "... and a
// jobs.gate.timeout event").
const (
	gateNamespace                     = "jobs.gate"
	EventGateDenied  events.EventKind = "jobs.gate.denied"
	EventGateTimeout events.EventKind = "jobs.gate.timeout"
)

// completionCheckTimedOutReason is handleCompletionHook's exact timeout
// deny reason, and the sentinel recordDenial uses to pick EventGateTimeout.
const completionCheckTimedOutReason = "completion check timed out"

// CompletionHookPayload is the completion-gate hook's own wire shape:
// which harness event fired (TaskCompleted or Stop), the harness's own
// session id, and the Cascade job/task identity a Cascade-dispatched
// agent's environment carries (CASCADE_JOB_ID; see
// pkg/provider.driverEnvAllowlistBase) but an ordinary human session
// never does -- an empty JobID/TaskID is the expected, non-error shape
// for the latter (completion_scope.go's ResolveJobID).
//
// StopHookActive mirrors the real native Stop payload's field (capture:
// testdata/completion/stop_fixture.json); changes no decision below.
type CompletionHookPayload struct {
	JobID          string        `json:"job_id"`
	TaskID         string        `json:"task_id"`
	SessionID      string        `json:"session_id"`
	EventType      HookEventType `json:"event_type"`
	StopHookActive bool          `json:"stop_hook_active,omitempty"`
}

// CompletionHookResponse is handleCompletionHook's outcome. The zero
// value (Deny=false) is "allow, proceed" -- both the unscoped "no
// opinion" case and a real completion-check pass return it, which is
// correct: a harness treats "no opinion" and "explicitly allowed"
// identically (it proceeds either way), and the ONLY thing that must
// never be ambiguous is a denial's reason.
type CompletionHookResponse struct {
	Deny   bool   `json:"deny"`
	Reason string `json:"reason,omitempty"`
}

// PolicyGate is the injected completion-check seam. The real
// implementation wraps *jobs.CompletionPolicy.Transition (constructed at
// daemon startup, cmd/cascade/hooks.go) -- no concrete type lives in this
// file. ok=true means the job may advance; ok=false with a non-empty
// reason means a real policy denial (jobs.ErrorCompletionDenied.Error(),
// verbatim); a non-nil err means the gate itself could not be evaluated
// (also folded into Deny by handleCompletionHook -- R-16.16 fails closed
// on a gate error exactly as it does on a timeout).
type PolicyGate interface {
	CompletionCheck(ctx context.Context, jobID string) (ok bool, reason string, err error)
}

// ErrMalformedCompletionPayload is ParseCompletionHookPayload's typed
// refusal for input that is not valid JSON or carries a field this
// struct's four-field allowlist does not declare.
var ErrMalformedCompletionPayload = cascade.New(cascade.KindInvalidInput, "hookpacks: malformed completion hook payload")

// ParseCompletionHookPayload decodes r into a CompletionHookPayload,
// rejecting any field outside the four-field allowlist and any input
// that is not valid JSON at all. It never panics: every decode failure
// returns ErrMalformedCompletionPayload wrapped with the underlying
// reason.
func ParseCompletionHookPayload(r io.Reader) (CompletionHookPayload, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var p CompletionHookPayload
	if err := dec.Decode(&p); err != nil {
		return CompletionHookPayload{}, cascade.Wrap(cascade.KindInvalidInput, err, ErrMalformedCompletionPayload.Error())
	}
	return p, nil
}

// handleCompletionHook is the R-16.16/R-21.176 decision itself. Scope is
// resolved FIRST (completion_scope.go): unscoped means allow with no
// opinion, and policy.completion_check is never called. Once scoped,
// every outcome but a clean pass is Deny: an unresolvable job id, a
// timeout, a gate error, or the gate's own denial.
func handleCompletionHook(ctx context.Context, payload CompletionHookPayload, gate PolicyGate, resolver JobResolver, timeout time.Duration) CompletionHookResponse {
	jobID, scoped, err := ResolveJobID(ctx, payload, resolver)
	if !scoped {
		// Either nothing in payload names a job (an ordinary human
		// session -- the common case), or scope itself could not be
		// determined (err != nil, e.g. the daemon is unreachable). Both
		// are "no opinion", never a denial (R-21.176).
		return CompletionHookResponse{}
	}
	if err != nil {
		// Job-scoped but the id does not resolve to a real job: fail closed.
		return CompletionHookResponse{Deny: true, Reason: fmt.Sprintf("unknown job id %q", jobID)}
	}
	if gate == nil {
		return CompletionHookResponse{Deny: true, Reason: "completion gate is not configured"}
	}

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, reason, gateErr := gate.CompletionCheck(checkCtx, jobID)
	if checkCtx.Err() == context.DeadlineExceeded {
		return CompletionHookResponse{Deny: true, Reason: completionCheckTimedOutReason}
	}
	if gateErr != nil {
		return CompletionHookResponse{Deny: true, Reason: gateErr.Error()}
	}
	if !ok {
		if reason == "" {
			reason = "completion check denied"
		}
		return CompletionHookResponse{Deny: true, Reason: reason}
	}
	return CompletionHookResponse{}
}

// completionDenialRecord is EventGateDenied/EventGateTimeout's payload: a
// superset of internal/jobs.gateDeniedWirePayload's {job_id, session_id,
// ticket_id, reason, attempt} (byte-compatible field names/types, so the
// stall detector's default json.Unmarshal "ignore unknown fields" reads
// this identically to a Transition denial) plus this package's own
// pre-existing HookEvent/TimestampMs audit fields. Attempt is always 0:
// the hook layer has no retry-attempt counter of its own.
type completionDenialRecord struct {
	JobID       string        `json:"job_id"`
	SessionID   string        `json:"session_id"`
	TicketID    string        `json:"ticket_id"`
	Attempt     int           `json:"attempt"`
	HookEvent   HookEventType `json:"hook_event"`
	Reason      string        `json:"reason"`
	TimestampMs int64         `json:"timestamp_ms"`
}

// recordDenial best-effort publishes one completionDenialRecord on
// EventGateDenied, or EventGateTimeout when reason is the exact timeout
// sentinel (completionCheckTimedOutReason). A nil bus or a Publish
// failure are both swallowed for the identical reason handler.go's
// publishJob documents: journaling a denial must never be the reason a
// denial response itself fails to return.
func recordDenial(ctx context.Context, bus *events.Bus, payload CompletionHookPayload, jobID, reason string, clock runtime.Clock) {
	if bus == nil {
		return
	}
	raw, err := json.Marshal(completionDenialRecord{
		JobID: jobID, SessionID: payload.SessionID, TicketID: payload.TaskID,
		HookEvent: payload.EventType, Reason: reason, TimestampMs: clock.Now().UnixMilli(),
	})
	if err != nil {
		return
	}
	kind := EventGateDenied
	if reason == completionCheckTimedOutReason {
		kind = EventGateTimeout
	}
	_, _ = bus.Publish(ctx, gateNamespace, kind, "hookpacks:completion-gate", raw)
}

// RegisterCompletionHookPack builds and registers the "completion-gate"
// HookPack (R-16.48): one descriptor each for TaskCompleted and Stop.
// gate/resolver are validated non-nil here (a pack registered over a nil
// gate would be a lie -- see handleCompletionHook's own nil-gate deny) but
// are not embedded in the rendered command template itself, which carries
// no Go closures; the real dispatch they drive is
// RegisterCompletionCheckHandler's RPC-side binding.
func RegisterCompletionHookPack(reg *HookRegistry, gate PolicyGate, resolver JobResolver) error {
	if reg == nil {
		return cascade.New(cascade.KindInvalidInput, "hookpacks: RegisterCompletionHookPack requires a HookRegistry")
	}
	if gate == nil || resolver == nil {
		return cascade.New(cascade.KindInvalidInput, "hookpacks: RegisterCompletionHookPack requires a non-nil gate and resolver")
	}
	reg.RegisterPack("completion-gate", HookPack{
		Name: "completion-gate",
		Descriptors: []HookDescriptor{
			{EventType: EventTaskCompleted, Matcher: "", CommandTemplate: completionHookCommand(EventTaskCompleted, defaultCompletionTimeout)},
			{EventType: EventStop, Matcher: "", CommandTemplate: completionHookCommand(EventStop, defaultCompletionTimeout)},
		},
	})
	return nil
}

// RegisterCompletionCheckHandler binds MethodCompletionCheck to registry:
// the RPC-side counterpart RegisterCompletionHookPack's rendered commands
// call back into. A zero timeout uses defaultCompletionTimeout.
func RegisterCompletionCheckHandler(registry *rpc.Registry, clock runtime.Clock, bus *events.Bus, gate PolicyGate, resolver JobResolver, timeout time.Duration) error {
	if registry == nil {
		return cascade.New(cascade.KindInvalidInput, "hookpacks: RegisterCompletionCheckHandler requires an *rpc.Registry")
	}
	if timeout <= 0 {
		timeout = defaultCompletionTimeout
	}
	registry.Register(MethodCompletionCheck, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := ParseCompletionHookPayload(bytes.NewReader(raw))
		if err != nil {
			resp := CompletionHookResponse{Deny: true, Reason: "malformed completion hook payload"}
			recordDenial(ctx, bus, payload, "", resp.Reason, clock)
			return resp, nil
		}
		resp := handleCompletionHook(ctx, payload, gate, resolver, timeout)
		if resp.Deny {
			jobID, _, _ := ResolveJobID(ctx, payload, resolver)
			recordDenial(ctx, bus, payload, jobID, resp.Reason, clock)
		}
		return resp, nil
	})
	return nil
}
