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
	goruntime "runtime"

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
	// Privacy is the §5.16 tier NAME this thread should be created under
	// ("local-only" or "restricted"), or "" to send none and let the
	// daemon apply its own fail-closed default. Only meaningful when
	// Thread is empty: that is the request that creates a thread.
	Privacy string
	// LeadingContext is a resume's composed block, staged by
	// applyChatResume or the /resume command for delivery as the leading
	// block of THIS turn: the daemon stores it as segment 0, ahead of
	// Prompt. Sent exactly once, then cleared. Empty is the ordinary case.
	LeadingContext string
}

// OneShotResult is the adapter's reply to a OneShotRequest: the identifiers
// --json emits, plus the assistant's full response content.
type OneShotResult struct {
	TurnID   string
	ThreadID string
	Content  string
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
	// private and localOnly are the 07-CLI-COMMAND-TREE per-thread privacy
	// modes. They are mutually exclusive and BOTH optional: neither one
	// set is not "no privacy", it is §5.16's fail-closed restricted, which
	// the daemon applies whether or not this command says so.
	private   bool
	localOnly bool
	// topics/threads/page/pageSize: U/S-46.T4 listing surfaces. They
	// bypass the Client above entirely; see topics_cli.go.
	topics   bool
	threads  bool
	page     int
	pageSize int
	// resumeTopic/resumeQuery: V/S-47.T2 bring-into-chat resume. Mutually
	// exclusive (validateResumeFlags, resume.go); neither set is ordinary
	// chat.
	resumeTopic string
	resumeQuery string
	// leadingContext is a resolved resume's block, staged by
	// applyChatResume for the next dispatch; "" is the ordinary case.
	leadingContext string
	// threadMinted is true when this session CREATED its thread
	// (applyChatResume via chat.create_thread) rather than the operator
	// naming one with --thread; maybeWriteThreadSummary (topics_cli.go)
	// reads it so a freshly created thread is not mistaken for an "open an
	// existing thread" request, and dispatchOneShot sends no privacy_mode
	// for it (its marker was written at creation).
	threadMinted bool
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
			if _, err := resolveChatPrivacy(opts); err != nil {
				return err
			}
			if err := validateResumeFlags(opts); err != nil {
				return err
			}
			return runChat(cc, opts, osLookupEnv)
		},
	}
	c.Flags().StringVarP(&opts.prompt, "message", "m", "", "one-shot prompt (equivalent to the positional prompt)")
	c.Flags().StringVar(&opts.thread, "thread", "", "continue an existing thread by id")
	c.Flags().BoolVar(&opts.json, "json", false, "emit a structured JSON result: {turn_id, thread_id, content}")
	c.Flags().BoolVar(&opts.quiet, "quiet", false, "suppress metadata headers in one-shot output")
	c.Flags().BoolVar(&opts.private, "private", false,
		"create the thread restricted: its content may not ride a lane whose destination cannot be resolved")
	c.Flags().BoolVar(&opts.localOnly, "local-only", false,
		"create the thread local-only: its content may never leave this machine")
	c.Flags().BoolVar(&opts.topics, "topics", false, "list active topics with thread counts, then exit")
	c.Flags().BoolVar(&opts.threads, "threads", false, "list threads (title, slug, message count), paginated, then exit")
	c.Flags().IntVar(&opts.page, "page", 1, "--threads: page number (1-based)")
	c.Flags().IntVar(&opts.pageSize, "page-size", 20, "--threads: threads per page")
	c.Flags().StringVar(&opts.resumeTopic, "resume-topic", "",
		"bring a topic thread's composed context into this session as the leading context block, then continue it")
	c.Flags().StringVar(&opts.resumeQuery, "resume-query", "",
		"bring a recall.what query's composed context into this session as the leading context block")
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

	// U/S-46.T4: --topics/--threads are read-only, TTY-independent
	// listing surfaces; run before CASCADE_NO_INPUT/TUI logic so neither
	// blocks them (automation parity).
	if opts.topics {
		return runTopicsList(ctx, cc, opts.json)
	}
	if opts.threads {
		return runThreadsList(ctx, cc, opts.json, opts.page, opts.pageSize)
	}

	// The no-prompt refusals run BEFORE any daemon call: a session that
	// cannot proceed never creates a thread.
	if err := refuseNoPromptFreshSession(opts, lookupEnv); err != nil {
		return err
	}
	if err := applyChatResume(ctx, cc, &opts); err != nil {
		return err
	}

	// --thread <slug> with no prompt is "open" mode; see
	// maybeWriteThreadSummary's own doc comment for the non-interactive
	// parity rule this enforces.
	done, err := maybeWriteThreadSummary(ctx, cc, opts, lookupEnv)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	if opts.prompt == "" {
		if v, ok := lookupEnv("CASCADE_NO_INPUT"); ok && v == "1" {
			return errChatNoInputNoPrompt
		}
		if goruntime.GOOS == "windows" {
			return errChatWindowsTUIRefusal
		}
		if err := refusePrivacyOnNamedThread(opts); err != nil {
			return err
		}
		return runTUI(tuiContext(ctx, opts), cc, opts.thread)
	}

	return dispatchOneShot(ctx, cc, opts)
}

// dispatchOneShot/refuseNoPromptFreshSession/tuiContext/writeOneShotResult
// live in chat_dispatch.go under Art.10.3's 300-line cap.
