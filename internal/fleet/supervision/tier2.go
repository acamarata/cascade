// Purpose: tier 2, PTY-attached — the supervision tier that holds every
//
//	action at risk level L2 or above until a person approves it on an
//	attached pseudo-terminal, and fails closed on anything else.
//
// WHAT "FAILS CLOSED" MEANS HERE, PRECISELY. A denial, a prompt that could
//
//	not be shown, a terminal that detached mid-question and an environment
//	with nobody at the keyboard all reach the same answer: the action does
//	not run. They differ only in what the operator is told. That is
//	fail-closed on AUTHORIZATION — the question "did a human approve this"
//	has no yes — and is not the fail-closed-on-preference R-14.245
//	separates it from.
//
// WHY THE ATTACHER IS A SEAM. Attaching a real pseudo-terminal is a
//
//	platform capability, not a policy decision: it exists on darwin and
//	linux and does not exist on Windows (06-FORGE-SPEC §2). The decision
//	half lives here, once, on every platform; the attaching half lives in
//	tier2_unix.go and tier2_windows.go, and the Windows one is a real
//	refusal rather than a stub — an operator there is told to pick tier 1
//	or tier 3.
//
// Inputs: an attacher, an environment reader, and the audit/attention
//
//	sinks.
//
// Outputs: nil when a human approved; a typed refusal otherwise.
// Constraints: no bare os.Stdin/os.Stdout — the prompt is written to the
//
//	streams the attacher returns. Nothing here sleeps or polls; the prompt
//	blocks on the terminal and on ctx, whichever answers first.
//
// SPORT: fleet.supervision.Tier2Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import (
	"context"
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrPTYUnavailable is what tier 2 returns where no pseudo-terminal can be
// attached. It is the Windows answer (06-FORGE-SPEC §2) and it names the
// two tiers that DO work there, because a refusal an operator cannot act
// on is the one answer that helps nobody.
var ErrPTYUnavailable = cascade.New(cascade.KindUnsupported,
	"supervision: tier 2 needs a pseudo-terminal, which this platform does not provide; "+
		"select tier 1 (hook-mediated) or tier 3 (suggest-only) in [fleet.supervision].tier")

// ErrSupervisionDenied is the refusal a human's "no" produces. It is a
// distinct sentinel from ErrPTYUnavailable so a caller can tell "somebody
// said no" from "nobody could be asked" — the first is a decision, the
// second is a capability gap, and conflating them would report an
// operator's judgement as an environment problem.
var ErrSupervisionDenied = cascade.New(cascade.KindPolicyDenied,
	"supervision: tier 2 approval was denied at the attached terminal")

// noInputEnv is the environment variable that declares nobody is at the
// keyboard.
const noInputEnv = "CASCADE_NO_INPUT"

// NoInputDenyReason is the logged reason a tier-2 approval auto-denies
// under CASCADE_NO_INPUT=1. Exported so a test asserts the wording rather
// than a substring of it.
const NoInputDenyReason = "NO_INPUT mode"

// tier2Actor names tier 2 in the audit log.
const tier2Actor = "supervision.tier2"

// tier2MinHeldLevel is the rung at and above which tier 2 holds an action.
// L2 is 06-FORGE-SPEC §5.15's workspace-mutation rung: below it an action
// changes nothing a person would want to be woken for, and holding those
// too would train the operator to approve without reading.
const tier2MinHeldLevel = policy.L2

// Session is one attached pseudo-terminal: the streams the prompt is
// written to and read from, and the way to let it go.
type Session struct {
	// In is the terminal's input side — what the person types.
	In io.Reader
	// Out is the terminal's output side — where the question is shown.
	Out io.Writer
	// Close releases the terminal. It is never nil on a successful attach.
	Close func() error
}

// PTYAttacher opens a pseudo-terminal for one supervised session.
//
// Implemented by the platform files: tier2_unix.go attaches a real OS
// pseudo-terminal, and tier2_windows.go refuses with ErrPTYUnavailable.
type PTYAttacher interface {
	Attach(ctx context.Context) (*Session, error)
}

// Tier2Supervisor holds L2-and-above actions pending a human's answer on
// an attached terminal.
type Tier2Supervisor struct {
	attach PTYAttacher
	getenv runtime.Getenv
	// queue and trace may be nil; a shorter trail, never a different
	// answer.
	queue AttentionPusher
	trace audit.Writer
}

// NewTier2Supervisor builds the PTY-attached supervisor.
//
// The attacher and the environment reader are required. An attacher this
// could not call would have to answer approval questions with no terminal
// at all, and the only safe answer it could give is deny — which is tier
// 2 refusing everything while reporting itself as working.
func NewTier2Supervisor(attach PTYAttacher, getenv runtime.Getenv,
	queue AttentionPusher, trace audit.Writer) (*Tier2Supervisor, error) {
	switch {
	case attach == nil:
		return nil, cascade.New(cascade.KindInvalidInput,
			"supervision: tier 2 requires a pseudo-terminal attacher")
	case getenv == nil:
		return nil, cascade.New(cascade.KindInvalidInput,
			"supervision: tier 2 requires an environment reader")
	}
	return &Tier2Supervisor{attach: attach, getenv: getenv, queue: queue, trace: trace}, nil
}

// Tier reports which tier this supervisor is.
func (s *Tier2Supervisor) Tier() Tier { return TierPTYAttached }

// Holds reports whether tier 2 holds an action at this rung.
//
// It is exported so the dispatch point can ask before attaching anything:
// attaching a terminal to ask about an L0 read would be a prompt nobody
// needs and a cost every action pays.
func (s *Tier2Supervisor) Holds(level policy.RiskLevel) bool {
	return level >= tier2MinHeldLevel
}

// Run attaches the pseudo-terminal for a supervised session and returns it.
//
// On a platform with no pseudo-terminal this returns ErrPTYUnavailable and
// NO session: the session is not started, rather than started and then
// found to be unusable at the first question.
func (s *Tier2Supervisor) Run(ctx context.Context) (*Session, error) {
	sess, err := s.attach.Attach(ctx)
	if err != nil {
		return nil, err
	}
	if sess == nil || sess.In == nil || sess.Out == nil || sess.Close == nil {
		return nil, cascade.New(cascade.KindInternal,
			"supervision: tier 2 attached an incomplete session")
	}
	return sess, nil
}

// Approve asks the person at sess whether req may run, and returns nil
// only if they said yes.
//
// Every other path returns an error and the action does not run:
//   - CASCADE_NO_INPUT=1 auto-denies without asking, because there is
//     nobody to ask and a question written to a terminal nobody reads is
//     an approval that would hang forever;
//   - a prompt that could not be written, or an answer that could not be
//     read — a terminal detached mid-question — denies;
//   - an explicit "no" denies.
//
// An action BELOW the held rung returns nil without asking. That is not a
// bypass: tier 2's contract is to hold L2 and above, and asking about
// everything would train the operator to stop reading.
func (s *Tier2Supervisor) Approve(ctx context.Context, sess *Session,
	req policy.EvalRequest, out policy.EvalOutcome) error {
	if !s.Holds(out.Level) {
		return nil
	}
	if s.getenv(noInputEnv) == "1" {
		return s.deny(ctx, req, out, NoInputDenyReason)
	}
	if sess == nil || sess.In == nil || sess.Out == nil {
		return s.deny(ctx, req, out, "no attached terminal")
	}
	approved, err := askOnTerminal(ctx, sess, req, out)
	if err != nil {
		return s.deny(ctx, req, out, "the terminal could not be asked: "+err.Error())
	}
	if !approved {
		return s.deny(ctx, req, out, "denied at the terminal")
	}
	s.emit(ctx, req, out, "approved", "")
	return nil
}

// deny records the refusal and returns it. Every fail-closed path above
// funnels through here, so the audit row and the attention item cannot
// diverge between them.
func (s *Tier2Supervisor) deny(ctx context.Context, req policy.EvalRequest,
	out policy.EvalOutcome, reason string) error {
	s.emit(ctx, req, out, "denied", reason)
	s.queueDenied(ctx, actionRef(req))
	return cascade.Wrapf(cascade.KindPolicyDenied, ErrSupervisionDenied,
		"supervision: tier 2 did not approve this action (%s)", reason)
}

// queueDenied files the refusal for a human. A push failure is not
// returned: the refusal is already the caller's answer, and replacing it
// with a queue error would report a different thing than what happened.
func (s *Tier2Supervisor) queueDenied(ctx context.Context, ref string) {
	if s.queue == nil {
		return
	}
	_, _ = s.queue.Push(ctx, AttentionItem{
		Kind:      KindPolicyAsk,
		SourceRef: ref,
		ScopeRef:  ScopeRef{Kind: scope.ScopeKindGlobal},
		Priority:  autoAdvancePriorityAsk,
	})
}

// tier2Trace is the audit row's rationale payload: what was asked, at what
// rung, and what came back. It carries no command text, on the same terms
// as dryrun_trace.go's row.
type tier2Trace struct {
	Ref        string `json:"ref"`
	Verb       string `json:"verb"`
	Capability string `json:"capability"`
	RiskLevel  string `json:"risk_level"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
}

// emit appends the decision to the append-only log. A failed append is
// ignored: the decision is already made, and a logging error must not
// become a different answer.
func (s *Tier2Supervisor) emit(ctx context.Context, req policy.EvalRequest,
	out policy.EvalOutcome, outcome, reason string) {
	if s.trace == nil {
		return
	}
	payload, err := json.Marshal(tier2Trace{
		Ref: actionRef(req), Verb: req.Verb, Capability: req.Capability,
		RiskLevel: out.Level.String(), Outcome: outcome, Reason: reason,
	})
	if err != nil {
		return
	}
	_, _ = s.trace.Append(ctx, audit.Event{
		Kind:       audit.KindPolicyRoute,
		Actor:      tier2Actor,
		Action:     actionRef(req),
		ParamsHash: audit.HashParams(req.Params),
		RiskLevel:  out.Level.String(),
		Verdict:    policy.VerdictAsk.String(),
		Outcome:    outcome,
		Explain:    payload,
	})
}
