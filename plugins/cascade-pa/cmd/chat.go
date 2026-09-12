// Package cmd implements `cascade chat`'s cobra surface: one-shot mode
// (send a single turn, stream the response, exit) and, with no prompt
// given, interactive TUI mode (chat_tui.go). Both modes talk to the daemon
// only through the Client seam below — never directly through
// internal/client or internal/conversation — because plugins/** may import
// pkg/** only, never internal/** (Art.10.2, plugins-providers-boundary).
//
// COMPOSITION-ROOT DEVIATION (recorded, not papered over, matching
// internal/conversation/adapter.go's identical precedent for its own
// out-of-scope RegisterHandlers call): the real call to SetClient, wiring
// this seam to internal/client's unix-socket JSON-RPC transport, belongs in
// the binary's composition root (internal/plugins/registry.go or
// cmd/cascade/root.go). Neither file is in this ticket's files_scope, and
// root.go is under concurrent edit by another agent this phase, so this
// ticket does not add that call site. Recorded in
// internal/build/testonly-allow.json under this ticket's id, exactly as
// R-21.273's precedent directs. Until that call lands, GetClient returns
// unconfiguredClient, whose typed cascade.KindUnavailable error is exactly
// the "daemon unreachable" behavior the ticket's acceptance criteria
// require — never a blank screen, a hang, or a silent retry loop.
//
// SPORT: plugins/cascade-pa:cmd:chat (ADD) — P1-E20-W5-S43-T3.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// envLookup abstracts os.LookupEnv so tests never depend on process-global
// environment state; production always passes the real os.LookupEnv from
// commands.go's caller through NewChatCommand's default.
type envLookup func(key string) (string, bool)

// OneShotRequest is one turn's worth of input to the chat adapter: a
// prompt and, optionally, an existing thread id to continue.
type OneShotRequest struct {
	// Thread is the thread slug/id to continue, or "" to start a new
	// thread.
	Thread string
	// Prompt is the user's message text.
	Prompt string
}

// OneShotResult is the adapter's reply to a OneShotRequest: the identifiers
// --json emits, plus the assistant's full response content.
type OneShotResult struct {
	TurnID   string
	ThreadID string
	Content  string
}

// Client is the seam over the daemon's chat adapter (T2's
// internal/conversation.Adapter, reached over the unix socket via
// internal/client): OneShot performs a single non-streaming turn (used by
// one-shot mode); Stream performs the same turn but delivers the response
// incrementally over the returned channel (used by TUI mode). Both take a
// context so a caller can cancel a live request (Ctrl-C mid-stream).
type Client interface {
	// OneShot sends req and returns the complete reply, or a typed
	// cascade error (KindUnavailable when the daemon cannot be reached,
	// KindNotFound when req.Thread does not exist, KindInvalidInput for a
	// malformed request).
	OneShot(ctx context.Context, req OneShotRequest) (OneShotResult, error)
	// Stream sends req and delivers the reply incrementally: tokens on
	// the first channel (closed when the stream ends normally), a single
	// terminal error (or nil) on the second channel once the first
	// closes. Canceling ctx aborts the stream; Stream must still close
	// both channels promptly in that case rather than leaking the
	// goroutine that feeds them.
	Stream(ctx context.Context, req OneShotRequest) (<-chan string, <-chan error)
}

// unconfiguredClient is the default Client: every call fails with a typed,
// actionable KindUnavailable error naming the missing wiring. This is not
// a stub standing in for unfinished work (Art.1) — it is the correct,
// deliberate behavior of a real binary that has not called SetClient, and
// its error is exactly what the daemon-unreachable acceptance criterion
// requires: never a blank screen, a hang, or a silent retry loop.
type unconfiguredClient struct{}

// errClientUnconfigured is the shared error unconfiguredClient returns.
var errClientUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade chat: no daemon adapter is wired into this binary; "+
		"start the daemon with `cascade daemon run` and ensure cascade-pa's "+
		"client wiring is configured")

func (unconfiguredClient) OneShot(context.Context, OneShotRequest) (OneShotResult, error) {
	return OneShotResult{}, errClientUnconfigured
}

func (unconfiguredClient) Stream(context.Context, OneShotRequest) (<-chan string, <-chan error) {
	tokens := make(chan string)
	errs := make(chan error, 1)
	close(tokens)
	errs <- errClientUnconfigured
	close(errs)
	return tokens, errs
}

// clientState guards the package-level Client seam so SetClient is safe
// under concurrent registration/test use.
var clientState struct {
	mu sync.RWMutex
	c  Client
}

// SetClient injects the real Client implementation. Intended to be called
// exactly once, by the composition root, before any `cascade chat`
// invocation — see this file's package doc comment for why that call site
// is a recorded, out-of-scope deviation in this ticket. Tests call it
// directly to inject a fake.
func SetClient(c Client) {
	clientState.mu.Lock()
	clientState.c = c
	clientState.mu.Unlock()
}

// activeClient returns the configured Client, or unconfiguredClient{} if
// SetClient has never been called.
func activeClient() Client {
	clientState.mu.RLock()
	defer clientState.mu.RUnlock()
	if clientState.c == nil {
		return unconfiguredClient{}
	}
	return clientState.c
}

// errChatWindowsTUIRefusal is TUI mode's unconditional Windows tier-2
// refusal (06-FORGE-SPEC.md §2): Windows tier-2 has no PTY contract this
// package can rely on, so TUI mode refuses before touching the Client at
// all. One-shot mode is unaffected — see chat_windows_test.go
// (//go:build windows) for the CI-asserted proof (18-T0-RULINGS-R16.md
// R-16.47: asserted by a build-tagged test, never GOOS=windows go test).
var errChatWindowsTUIRefusal = cascade.New(cascade.KindUnsupported,
	"cascade chat: TUI unsupported on Windows (tier-2)")

// errChatNoInputNoPrompt is CASCADE_NO_INPUT=1's refusal when no prompt was
// given (positional or -m): 06-FORGE-SPEC.md §5 rule 8 / 08-INIT-CONFIG-
// SPEC.md §CASCADE_NO_INPUT automation parity.
var errChatNoInputNoPrompt = cascade.New(cascade.KindInvalidInput,
	"cascade chat: no prompt given and CASCADE_NO_INPUT=1; pass a prompt "+
		"positionally or via -m")

// chatOptions is NewChatCommand's parsed flag/arg state, threaded into
// runChat as a single value so runChat itself takes no *cobra.Command flag
// pointers and is trivial to unit test.
type chatOptions struct {
	prompt string
	thread string
	json   bool
	quiet  bool
}

// NewChatCommand builds the `chat` cobra command cascade-pa's commands.go
// executes against the raw args its BuiltinHandlers.RunCommand receives.
// This command owns its own flag set (-m/--thread/--json/--quiet) rather
// than relying on any pre-parsing the mounting command may have performed,
// so it behaves identically whether invoked through the real binary's
// composition root or directly in a test.
func NewChatCommand() *cobra.Command {
	var opts chatOptions
	c := &cobra.Command{
		Use:   "chat [prompt]",
		Short: "Chat with the cascade assistant (interactive TUI or one-shot)",
		Long: "With a positional prompt or -m/--message, sends one turn to the daemon, " +
			"streams the reply to stdout, and exits. With neither, opens an interactive " +
			"terminal UI. CASCADE_NO_INPUT=1 with no prompt and no -m exits non-zero " +
			"instead of opening the TUI (automation parity).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cc *cobra.Command, args []string) error {
			if opts.prompt == "" && len(args) > 0 {
				opts.prompt = args[0]
			}
			return runChat(cc, opts, osLookupEnv)
		},
	}
	c.Flags().StringVarP(&opts.prompt, "message", "m", "", "one-shot prompt (equivalent to the positional prompt)")
	c.Flags().StringVar(&opts.thread, "thread", "", "continue an existing thread by id")
	c.Flags().BoolVar(&opts.json, "json", false, "emit a structured JSON result: {turn_id, thread_id, content}")
	c.Flags().BoolVar(&opts.quiet, "quiet", false, "suppress metadata headers in one-shot output")
	return c
}

// runChat is NewChatCommand's real logic, factored out so tests drive it
// directly with a fake envLookup and a cobra.Command whose in/out streams
// are bytes.Buffer values — no TTY, no os.Environ, no tea.Program.
func runChat(cc *cobra.Command, opts chatOptions, lookupEnv envLookup) error {
	ctx := cc.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if opts.prompt == "" {
		if v, ok := lookupEnv("CASCADE_NO_INPUT"); ok && v == "1" {
			return errChatNoInputNoPrompt
		}
		if goruntime.GOOS == "windows" {
			return errChatWindowsTUIRefusal
		}
		return runTUI(ctx, cc, opts.thread)
	}

	client := activeClient()
	res, err := client.OneShot(ctx, OneShotRequest{Thread: opts.thread, Prompt: opts.prompt})
	if err != nil {
		return err
	}
	return writeOneShotResult(cc, res, opts)
}

// writeOneShotResult renders a completed OneShotResult to cc's own output
// stream (cc.OutOrStdout() — never a bare os.Stdout reference in this
// file, matching internal/build/outputgate.go's boundary) in one of three
// shapes: --json's structured object; --quiet's content-only line; or the
// default two-line "thread: <id>\nturn: <id>\n<content>" form.
func writeOneShotResult(cc *cobra.Command, res OneShotResult, opts chatOptions) error {
	out := cc.OutOrStdout()
	if opts.json {
		enc := json.NewEncoder(out)
		return enc.Encode(struct {
			TurnID   string `json:"turn_id"`
			ThreadID string `json:"thread_id"`
			Content  string `json:"content"`
		}{TurnID: res.TurnID, ThreadID: res.ThreadID, Content: res.Content})
	}
	var b strings.Builder
	if !opts.quiet {
		fmt.Fprintf(&b, "thread: %s\nturn: %s\n", res.ThreadID, res.TurnID)
	}
	b.WriteString(res.Content)
	b.WriteString("\n")
	_, err := out.Write([]byte(b.String()))
	return err
}
