package cascadepa

// Purpose: the plugin.BuiltinHandlers implementation cascade-pa registers
//   in plugin.go's init(). RunCommand is the manifest-v2 provides.commands
//   mount point's dispatch target (18-T0-RULINGS-R16.md R-16.55): it builds
//   a real, fully-flagged *cobra.Command from this plugin's own cmd
//   subpackage and executes it against the raw args the host's mounted
//   cobra.Command passes through. cascade-pa provides no tools or intents
//   in this ticket, so DispatchTool/DispatchIntent return typed
//   not-found errors rather than panicking or silently succeeding
//   (Art.1 — no stubs that fake success).
// Inputs: a command/tool/intent name plus its raw argument bytes or
//   string slice, exactly as plugin.BuiltinHandlers requires.
// Outputs: RunCommand's real chat behavior (one-shot or TUI, per
//   cmd.NewChatCommand); typed cascade.KindNotFound errors for anything
//   this plugin does not provide.
// Constraints: pkg/plugin and pkg/cascade only besides this plugin's own
//   cmd subpackage (Art.10.2).
// SPORT: plugins/cascade-pa:cmd:chat (ADD) — P1-E20-W5-S43-T3.

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// InChatHandler is one command this plugin recognizes inside a live chat
// turn's own text — a structured trigger like `/soul edit ...` or
// `/memory review ...` — before that text would otherwise be sent to the
// assistant as an ordinary prompt. matched is false when text is not this
// handler's command at all, so DispatchInChatText can try the next one.
type InChatHandler func(ctx context.Context, text string) (reply string, matched bool, err error)

// inChatCommands is cascade-pa's internal command registry (V/S-47.T3 and
// V/S-47.T4): every in-chat command this plugin serves, tried in order.
// Neither entry is a new CLI noun, RPC method, or MCP tool — each is a
// presenter over an already-audited memory RPC, reached through this
// registry alone.
var inChatCommands = []InChatHandler{
	HandleSoulChatCommand,
	HandleReviewChatCommand,
}

// DispatchInChatText tries every registered in-chat command in turn. The
// first match wins; matched=false means text is not one of these commands
// and the caller should treat it as an ordinary chat prompt.
func DispatchInChatText(ctx context.Context, text string) (reply string, matched bool, err error) {
	for _, h := range inChatCommands {
		if reply, matched, err = h(ctx, text); matched {
			return reply, true, err
		}
	}
	return "", false, nil
}

// extractChatPrompt reads the prompt `cascade chat`'s own flags would
// resolve to (positional arg or -m/--message), by parsing args against a
// FRESH pacmd.NewChatCommand()'s real flag set rather than re-implementing
// that grammar here. ok is false when args do not parse as a prompt at
// all (e.g. -h), in which case the caller falls through to the ordinary
// chat command so its own error/help handling stays authoritative.
func extractChatPrompt(args []string) (prompt string, ok bool) {
	c := pacmd.NewChatCommand()
	if err := c.ParseFlags(args); err != nil {
		return "", false
	}
	if m, _ := c.Flags().GetString("message"); m != "" {
		return m, true
	}
	if rest := c.Flags().Args(); len(rest) > 0 {
		return rest[0], true
	}
	return "", false
}

// printInChatReply writes an in-chat command's reply through a fresh
// cobra.Command's own OutOrStdout — matching cmd/chat.go's
// writeOneShotResult, which is the sanctioned way this codebase reaches
// the real stdout without a bare os.Stdout/fmt.Print reference in this
// file (internal/build/outputgate.go).
func printInChatReply(reply string) error {
	c := &cobra.Command{}
	_, err := fmt.Fprintln(c.OutOrStdout(), reply)
	return err
}

// handlers is the real plugin.BuiltinHandlers implementation for
// cascade-pa. It carries no state: every method is a pure function of its
// arguments plus pacmd's package-level Client seam (see cmd/chat.go).
type handlers struct{}

// DispatchTool: cascade-pa provides no tools in this ticket.
func (handlers) DispatchTool(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, fmt.Sprintf("cascade-pa: unknown tool %q", name))
}

// DispatchIntent: cascade-pa provides no intents in this ticket.
func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, fmt.Sprintf("cascade-pa: unknown intent %q", name))
}

// RunCommand services the "chat" CommandSpec: it builds a fresh
// *cobra.Command from pacmd.NewChatCommand (which owns the -m/--thread/
// --json/--quiet flag surface and the one-shot-vs-TUI decision), sets args
// as its raw argument vector, and executes it. Any other command name is a
// typed not-found error — this plugin provides exactly one command.
func (handlers) RunCommand(ctx context.Context, name string, args []string) error {
	if name != commandChat {
		return cascade.New(cascade.KindNotFound, fmt.Sprintf("cascade-pa: unknown command %q", name))
	}
	if prompt, ok := extractChatPrompt(args); ok {
		if reply, matched, err := DispatchInChatText(ctx, prompt); matched {
			if err != nil {
				return err
			}
			return printInChatReply(reply)
		}
	}
	// A digest nudge is a convenience, never a blocker: a subscriber
	// failure (daemon unreachable, nothing wired) is swallowed rather
	// than turned into a failed chat turn (events.go's own contract).
	if nudge, shown, _ := CheckDigestNudge(ctx); shown {
		if err := printInChatReply(nudge); err != nil {
			return err
		}
	}
	c := pacmd.NewChatCommand()
	c.SetArgs(args)
	return c.ExecuteContext(ctx)
}
