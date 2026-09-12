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

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
// starts a new turn's context (canceled independently of the parent ctx,
// so a mid-stream Ctrl-C aborts only that turn), fires pumpStream in a
// goroutine against whatever senderRef currently points to, and returns
// the cancel func immediately. senderRef is a pointer so runTUI can build
// the submit closure BEFORE the tea.Program exists (Model needs submit at
// construction time; the Program needs the Model) and assign the real
// *tea.Program into *senderRef right after — the closure only
// dereferences senderRef when a user actually presses Enter, by which
// point the assignment has always already happened. Factored out from
// runTUI so chat_tui_run_test.go can drive it directly with a
// recordingSender, without ever constructing a tea.Program.
func newSubmitFunc(ctx context.Context, thread string, client Client, senderRef *msgSender) func(string) (tea.Cmd, func()) {
	return func(prompt string) (tea.Cmd, func()) {
		turnCtx, cancel := context.WithCancel(ctx)
		go pumpStream(turnCtx, *senderRef, client, OneShotRequest{Thread: thread, Prompt: prompt})
		return nil, cancel
	}
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
