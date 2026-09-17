package supervision

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): tier 2's single claim — an action at L2 or above
//   runs only if a person said yes — and the four ways that can fail,
//   every one of which lands on "did not run".
//
// What is NOT tested here: the real pseudo-terminal, which is
//   tier2_pty_test.go's job behind a build tag. These tests drive the
//   decision half over in-memory streams, because the decision half is the
//   part that must behave identically on every platform.
// SPORT: fleet.supervision tier-2 tests (ADD) — P1-E18-W4-S39-T3.

// scriptedAttacher hands back a session over in-memory streams, so the
// approve/deny logic can be driven without a terminal.
type scriptedAttacher struct {
	answer string
	out    *strings.Builder
	closed int
	err    error
}

func (a *scriptedAttacher) Attach(context.Context) (*Session, error) {
	if a.err != nil {
		return nil, a.err
	}
	a.out = &strings.Builder{}
	return &Session{
		In:    strings.NewReader(a.answer),
		Out:   a.out,
		Close: func() error { a.closed++; return nil },
	}, nil
}

// noEnv reports an empty environment.
func noEnv(string) string { return "" }

// noInputEnvSet reports CASCADE_NO_INPUT=1.
func noInputEnvSet(k string) string {
	if k == noInputEnv {
		return "1"
	}
	return ""
}

// tier2Request is the action under test.
func tier2Request() policy.EvalRequest {
	return policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectAgent, ID: "agent-2"},
		Capability: "hooks.shell",
		Verb:       "fs.write",
		Action:     "rm -rf /workspace",
		Attributes: map[string]string{"ref": "act-tier2"},
	}
}

// newTier2 wires a supervisor over a scripted terminal.
func newTier2(t *testing.T, answer string, getenv func(string) string) (*Tier2Supervisor, *scriptedAttacher, *capturingAttention, *capturingAudit) {
	t.Helper()
	att := &scriptedAttacher{answer: answer}
	queue := &capturingAttention{}
	trace := &capturingAudit{}
	s, err := NewTier2Supervisor(att, getenv, queue, trace)
	if err != nil {
		t.Fatal(err)
	}
	return s, att, queue, trace
}

// held is an outcome at the rung tier 2 holds.
func held() policy.EvalOutcome {
	return policy.EvalOutcome{Level: policy.L3, Reason: "workspace mutation"}
}

// TestAYesApprovesAndNothingElseDoes is the whole tier in one table. Only
// an explicit yes may approve: an approval parser that accepted anything
// but a recognised "no" would turn a stray keystroke, or a terminal that
// closed before the person answered, into consent.
func TestAYesApprovesAndNothingElseDoes(t *testing.T) {
	ctx := context.Background()
	for answer, wantApproved := range map[string]bool{
		"y\n":      true,
		"Y\n":      true,
		"yes\n":    true,
		"  yes \n": true,
		"n\n":      false,
		"no\n":     false,
		"\n":       false,
		"maybe\n":  false,
		"yep\n":    false,
		"":         false, // the terminal closed without an answer
	} {
		s, att, _, _ := newTier2(t, answer, noEnv)
		sess, err := s.Run(ctx)
		if err != nil {
			t.Fatalf("%q: attach: %v", answer, err)
		}
		err = s.Approve(ctx, sess, tier2Request(), held())
		if wantApproved && err != nil {
			t.Errorf("%q: err = %v, want an approval", answer, err)
		}
		if !wantApproved && err == nil {
			t.Errorf("%q: approved; only an explicit yes may approve", answer)
		}
		if !wantApproved && !errors.Is(err, ErrSupervisionDenied) {
			t.Errorf("%q: err = %v, want it to wrap ErrSupervisionDenied", answer, err)
		}
		_ = att
	}
}

// TestTheQuestionNamesWhatIsBeingApproved holds the prompt to its job. A
// person asked "Approve? [y/N]" with nothing else on screen is not
// approving an action, they are approving a habit.
func TestTheQuestionNamesWhatIsBeingApproved(t *testing.T) {
	ctx := context.Background()
	s, att, _, _ := newTier2(t, "n\n", noEnv)
	sess, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Approve(ctx, sess, tier2Request(), held())

	shown := att.out.String()
	for _, want := range []string{"act-tier2", "fs.write", "hooks.shell", policy.L3.String(), "workspace mutation"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the prompt does not show %q:\n%s", want, shown)
		}
	}
}

// TestNoInputAutoDeniesWithoutAsking is the non-interactive rule. A
// question written to a terminal nobody reads is not a refusal, it is a
// hang — so tier 2 answers it itself, closed, and says why.
func TestNoInputAutoDeniesWithoutAsking(t *testing.T) {
	ctx := context.Background()
	s, att, queue, trace := newTier2(t, "y\n", noInputEnvSet)
	sess, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Approve(ctx, sess, tier2Request(), held())
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied", err)
	}
	if !strings.Contains(err.Error(), NoInputDenyReason) {
		t.Errorf("err = %q, want it to name %q", err, NoInputDenyReason)
	}
	// Nothing was asked: an approval that WOULD have been given is not
	// consulted, because there is nobody to give it.
	if att.out.Len() != 0 {
		t.Errorf("a question was written under CASCADE_NO_INPUT=1:\n%s", att.out.String())
	}
	if len(queue.items) != 1 {
		t.Errorf("%d attention items, want 1 — a held action nobody can approve is a stall", len(queue.items))
	}
	if len(trace.events) != 1 || trace.events[0].Outcome != "denied" {
		t.Errorf("trace = %+v, want one denied row", trace.events)
	}
}

// TestBelowTheHeldRungNothingIsAsked keeps tier 2 from training the
// operator to approve without reading. An L0 read is not what a person
// attached a terminal for.
func TestBelowTheHeldRungNothingIsAsked(t *testing.T) {
	ctx := context.Background()
	s, att, queue, _ := newTier2(t, "n\n", noEnv)
	sess, err := s.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []policy.RiskLevel{policy.L0, policy.L1} {
		if err := s.Approve(ctx, sess, tier2Request(), policy.EvalOutcome{Level: level}); err != nil {
			t.Errorf("%v was held: %v", level, err)
		}
		if s.Holds(level) {
			t.Errorf("Holds(%v) = true, want false", level)
		}
	}
	for _, level := range []policy.RiskLevel{policy.L2, policy.L3, policy.L4} {
		if !s.Holds(level) {
			t.Errorf("Holds(%v) = false, want true", level)
		}
	}
	if att.out.Len() != 0 {
		t.Errorf("a question was asked below the held rung:\n%s", att.out.String())
	}
	if len(queue.items) != 0 {
		t.Errorf("%d items queued for actions nobody was asked about", len(queue.items))
	}
}

// TestADetachedTerminalDenies is the mid-question failure the contract
// names. A terminal that went away has not approved anything, and reading
// its silence as consent is the one outcome that would make tier 2 worse
// than no supervision at all.
func TestADetachedTerminalDenies(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newTier2(t, "", noEnv)
	// A session whose output side refuses every write: the question cannot
	// even be shown.
	sess := &Session{In: strings.NewReader("y\n"), Out: refusingWriter{}, Close: func() error { return nil }}
	err := s.Approve(ctx, sess, tier2Request(), held())
	if !errors.Is(err, ErrSupervisionDenied) {
		t.Fatalf("err = %v, want a denial", err)
	}
}

// TestACancelledContextDenies covers the person who walked away. The wait
// is bounded by ctx, and what it resolves to is a denial.
func TestACancelledContextDenies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _, _, _ := newTier2(t, "", noEnv)
	// A reader that never returns, so only ctx can end the wait.
	sess := &Session{In: blockingReader{}, Out: &strings.Builder{}, Close: func() error { return nil }}
	if err := s.Approve(ctx, sess, tier2Request(), held()); !errors.Is(err, ErrSupervisionDenied) {
		t.Fatalf("err = %v, want a denial", err)
	}
}

// TestAnUnattachableTerminalIsNotASession holds Run to its contract: the
// session is not started, rather than started and found unusable at the
// first question.
func TestAnUnattachableTerminalIsNotASession(t *testing.T) {
	ctx := context.Background()
	att := &scriptedAttacher{err: ErrPTYUnavailable}
	s, err := NewTier2Supervisor(att, noEnv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, runErr := s.Run(ctx)
	if !errors.Is(runErr, ErrPTYUnavailable) {
		t.Fatalf("err = %v, want ErrPTYUnavailable", runErr)
	}
	if sess != nil {
		t.Error("a failed attach returned a session")
	}
	// And with no session, an approval question still denies rather than
	// panicking on a nil.
	if err := s.Approve(ctx, nil, tier2Request(), held()); !errors.Is(err, ErrSupervisionDenied) {
		t.Errorf("Approve with no session: err = %v, want a denial", err)
	}
}

// TestTierTwoRefusesToBeBuiltIncomplete holds Art.1 at the constructor.
func TestTierTwoRefusesToBeBuiltIncomplete(t *testing.T) {
	if _, err := NewTier2Supervisor(nil, noEnv, nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("no attacher: err = %v, want KindInvalidInput", err)
	}
	if _, err := NewTier2Supervisor(&scriptedAttacher{}, nil, nil, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("no environment reader: err = %v, want KindInvalidInput", err)
	}
}

// refusingWriter fails every write.
type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) {
	return 0, cascade.New(cascade.KindUnavailable, "terminal: detached")
}

// blockingReader never returns, so only ctx can end a wait on it.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {} //nolint:staticcheck // deliberately never returns; the test ends the wait via ctx
}

// compile-time: the two doubles satisfy the io interfaces the session uses.
var (
	_ io.Writer = refusingWriter{}
	_ io.Reader = blockingReader{}
)
