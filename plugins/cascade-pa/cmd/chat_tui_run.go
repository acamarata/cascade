package cmd

// Purpose (this file): wires chat_tui.go's message-driven Model to a real
//
//	terminal and a real Client — the one place in this package that
//	constructs a tea.Program, matching internal/fleet/top.go's own split
//	between the pure Model (top.go) and its real wiring
//	(cmd/cascade/fleet_top.go).
//
// Inputs: a context (canceled aborts the whole TUI, not just one stream),
//
//	the mounting *cobra.Command (source of OutOrStdout/InOrStdin — never
//	a bare os.Stdout/os.Stdin reference in this file, per
//	internal/build/outputgate.go), and a thread id (may be "").
//
// Outputs: nil on a clean Ctrl-D/:q exit; a typed cascade error otherwise
//
//	(daemon unreachable, or a tea.Program-level I/O failure).
//
// Constraints: refuses before ever constructing a tea.Program when the
//
//	Client is unconfigured or the platform is Windows tier-2 — a typed,
//	immediate error rather than a blank screen or a hang.
//
// SPORT: plugins/cascade-pa:cmd:chat (ADD) — P1-E20-W5-S43-T3.

import (
	"context"
	goruntime "runtime"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// pendingLeadingContext holds the ONE leading-context block a completed
// /resume stages for the very next ordinary prompt. Mutex-guarded: the
// resume goroutine sets it while the Program's event loop takes it.
// Cleared on read, so a block is sent exactly once.
type pendingLeadingContext struct {
	mu   sync.Mutex
	text string
}

func (p *pendingLeadingContext) set(s string) {
	p.mu.Lock()
	p.text = s
	p.mu.Unlock()
}

func (p *pendingLeadingContext) takeAndClear() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.text
	p.text = ""
	return s
}

// msgSender is the one bubbletea method pumpStream needs: *tea.Program
// satisfies it automatically, and tests substitute a recording double so
// pumpStream is exercised with NO tea.Program, NO TTY, and NO real clock.
type msgSender interface {
	Send(tea.Msg)
}

// runTUI launches cascade chat's interactive mode. Repeats the Windows
// tier-2 check runChat already performs so a future direct caller of
// runTUI gets the same protection.
func runTUI(ctx context.Context, cc *cobra.Command, thread string) error {
	if goruntime.GOOS == "windows" {
		return errChatWindowsTUIRefusal
	}
	client := activeClient()
	if _, unconfigured := client.(unconfiguredClient); unconfigured {
		return errClientUnconfigured
	}

	var sender msgSender
	submit := newSubmitFunc(ctx, thread, client, &sender)
	model := NewModel(thread, submit)
	prog := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithOutput(cc.OutOrStdout()),
		tea.WithInput(cc.InOrStdin()),
	)
	sender = prog

	final, err := prog.Run()
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade chat: tui")
	}
	m, ok := final.(Model)
	if !ok {
		return nil
	}
	return m.Err()
}

// newSubmitFunc builds the closure Model.submit calls on every Enter: it
// starts a turn context (canceled independently of ctx, so Ctrl-C aborts
// only that turn), fires pumpStream -- or pumpResume for a "/resume topic
// <id>"/"/resume query <terms>" line -- in a goroutine against whatever
// senderRef points to, and returns the cancel func. senderRef is a pointer
// so runTUI can build the closure before the tea.Program exists. The
// session (resume_session.go) starts at thread, with the flag tier ctx
// carries (tuiContext): a /resume creates the session thread at that tier
// when there is none yet, and later turns go to that thread.
func newSubmitFunc(ctx context.Context, thread string, client Client, senderRef *msgSender) func(string) (tea.Cmd, func()) {
	session := newChatSession(thread, chatSessionTierFrom(ctx))
	pending := &pendingLeadingContext{}
	pending.set(resumeLeadingContextFrom(ctx))
	return func(prompt string) (tea.Cmd, func()) {
		turnCtx, cancel := context.WithCancel(ctx)
		if req, ok := parseResumeCommand(prompt); ok {
			req.Cwd = resumeCwd()
			go pumpResume(turnCtx, *senderRef, client, session, req, pending)
			return nil, cancel
		}
		go pumpStream(turnCtx, *senderRef, client, session.appendRequest(prompt, pending.takeAndClear()))
		return nil, cancel
	}
}

// pumpResume runs one resume into session (resumeIntoSession, the same
// call the flags make) and sends exactly one resumeDoneMsg or resumeErrMsg.
// It returns without sending when ctx was canceled first. On success the
// block is staged on pending BEFORE resumeDoneMsg is sent, so the next
// Enter (which the Program only processes after that message) takes it.
func pumpResume(ctx context.Context, prog msgSender, client Client, session *chatSession, req ResumeRequest, pending *pendingLeadingContext) {
	res, err := resumeIntoSession(ctx, client, session, req)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		prog.Send(resumeErrMsg{err: err})
		return
	}
	pending.set(res.LeadingContext)
	prog.Send(resumeDoneMsg{context: res.LeadingContext, trimReport: res.Trimmed})
}

// pumpStream reads client.Stream(req) and forwards each token, and the
// stream's terminal outcome, into prog as tokenMsg/streamDoneMsg/
// streamErrMsg values — mirroring cmd/cascade/fleet_top.go's
// pumpFleetTopSSE precedent of feeding a running Program from an external
// goroutine rather than a tea.Cmd iterator. Returns promptly when ctx is
// canceled (a mid-stream Ctrl-C), never leaking past that point.
func pumpStream(ctx context.Context, prog msgSender, client Client, req OneShotRequest) {
	tokens, errs := client.Stream(ctx, req)
	for tokens != nil || errs != nil {
		select {
		case t, ok := <-tokens:
			if !ok {
				tokens = nil
				continue
			}
			prog.Send(tokenMsg{text: t})
		case e, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if e != nil {
				prog.Send(streamErrMsg{err: e})
			} else {
				prog.Send(streamDoneMsg{})
			}
		case <-ctx.Done():
			return
		}
	}
}
