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

// completionDeniedNamespace/EventCompletionDenied are this file's own
// journaled-denial record (Art.10.6: "every Deny writes a journaled
// reason record"). internal/fleet/journal's Kind enum is closed at eight
// members with no member for a policy-hook denial (R-21.216), so this
// uses the SAME durable, replayable events.Bus mechanism handler.go's
// publishJob already relies on for its own audit trail -- Replay(ctx,
// namespace, 0) reads the persisted row back, not merely a subscription.
const completionDeniedNamespace = "hookpacks.completion"

// EventCompletionDenied is published exactly once per Deny response,
// carrying completionDenialRecord.
const EventCompletionDenied events.EventKind = "hookpacks.completion.denied"

// CompletionHookPayload is the completion-gate hook's own wire shape:
// which harness event fired (TaskCompleted or Stop), the harness's own
// session id, and the Cascade job/task identity a Cascade-dispatched
// agent's environment carries (CASCADE_JOB_ID; see
// pkg/provider.driverEnvAllowlistBase) but an ordinary human session
// never does -- an empty JobID/TaskID is the expected, non-error shape
// for the latter (completion_scope.go's ResolveJobID).
type CompletionHookPayload struct {
	JobID     string        `json:"job_id"`
	TaskID    string        `json:"task_id"`
	SessionID string        `json:"session_id"`
	EventType HookEventType `json:"event_type"`
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
		// Job-scoped (payload named a job) but the id does not resolve
		// to a real job: fail closed.
		return CompletionHookResponse{Deny: true, Reason: fmt.Sprintf("unknown job id %q", jobID)}
	}
	if gate == nil {
		return CompletionHookResponse{Deny: true, Reason: "completion gate is not configured"}
	}

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, reason, gateErr := gate.CompletionCheck(checkCtx, jobID)
	if checkCtx.Err() == context.DeadlineExceeded {
		return CompletionHookResponse{Deny: true, Reason: "completion check timed out"}
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

// completionDenialRecord is EventCompletionDenied's payload shape: the
// job id, which hook event triggered the check, the verbatim deny
// reason, and the instant it was recorded.
type completionDenialRecord struct {
	JobID       string        `json:"job_id"`
	HookEvent   HookEventType `json:"hook_event"`
	Reason      string        `json:"reason"`
	TimestampMs int64         `json:"timestamp_ms"`
}

// recordDenial best-effort publishes one completionDenialRecord. A nil
// bus or a Publish failure are both swallowed for the identical reason
// handler.go's publishJob documents: journaling a denial must never be
// the reason a denial response itself fails to return.
func recordDenial(ctx context.Context, bus *events.Bus, payload CompletionHookPayload, jobID, reason string, clock runtime.Clock) {
	if bus == nil {
		return
	}
	raw, err := json.Marshal(completionDenialRecord{
		JobID: jobID, HookEvent: payload.EventType, Reason: reason, TimestampMs: clock.Now().UnixMilli(),
	})
	if err != nil {
		return
	}
	_, _ = bus.Publish(ctx, completionDeniedNamespace, EventCompletionDenied, "hookpacks:completion-gate", raw)
}

// completionHookCommand builds one event's synchronous command template:
// a curl call that WAITS for the daemon's response (unlike
// sessionsPackCommand's fire-and-forget `|| true`) and translates a deny
// response into the harness's own blocking contract (a non-zero exit with
// the reason on stderr). $CASCADE_JOB_ID/$CASCADE_TICKET_ID/
// $CASCADE_SESSION_ID are the real environment a Cascade-dispatched
// driver inherits (pkg/provider.driverEnvAllowlistBase); an ordinary
// human session simply leaves them unset, which is the unscoped case
// completion_scope.go documents.
func completionHookCommand(evt HookEventType) string {
	return `r=$(curl -s -m 10 --unix-socket ` + socketPlaceholder +
		` -X POST http://cascade.sock/rpc -H "Content-Type: application/json"` +
		` -d '{"jsonrpc":"2.0","id":1,"method":"` + MethodCompletionCheck +
		`","params":{"event_type":"` + string(evt) + `","session_id":"'"$CASCADE_SESSION_ID"'",` +
		`"job_id":"'"$CASCADE_JOB_ID"'","task_id":"'"$CASCADE_TICKET_ID"'"}}' 2>/dev/null); ` +
		`case "$r" in *'"deny":true'*) echo "$r" | sed -n 's/.*"reason":"\([^"]*\)".*/\1/p' >&2; exit 2;; esac; exit 0`
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
			{EventType: EventTaskCompleted, Matcher: "", CommandTemplate: completionHookCommand(EventTaskCompleted)},
			{EventType: EventStop, Matcher: "", CommandTemplate: completionHookCommand(EventStop)},
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
