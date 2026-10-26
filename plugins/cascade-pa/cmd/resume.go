package cmd

// Purpose (this file): `cascade chat --resume-topic`/`--resume-query` and
//   the parsing and rendering the in-session `/resume` command
//   (chat_tui_run.go) shares with them. Both reach the SAME
//   resumeIntoSession -> Client.Resume call (resume_session.go), 06-FORGE-
//   SPEC §5 rule 8's automation parity.
// Inputs: chatOptions' resumeTopic/resumeQuery/thread/privacy flags, or a
//   parsed `/resume` line.
// Outputs: the composed block written to cc.OutOrStdout() (or
//   --json-encoded), trim-report warnings on cc.ErrOrStderr(), and the
//   block staged on opts.leadingContext for the session's NEXT append.
// Constraints: plugins/** imports pkg/** only, so the request/result
//   types below mirror the daemon's wire shape rather than importing it.
//   A privacy flag is never silently dropped: with no --thread it becomes
//   the created session thread's tier; with --thread it is refused before
//   any daemon call, because the named thread's marker is already fixed.
// SPORT: plugins/cascade-pa:cmd:resume (ADD) -- P1-E22-W5-S47-T2.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ResumeRequest is one resume call's client-side input (chat.resume's
// params). Thread is the DESTINATION session thread (session_thread_id);
// resumeIntoSession fills it.
type ResumeRequest struct {
	TopicID string
	Query   string
	Thread  string
	Cwd     string
}

// ResumeResult mirrors chat.resume's result {thread_id, leading_context,
// tokens, trimmed[]}: ThreadID is the resumed SOURCE thread; Trimmed holds
// one per-domain trim report line per budget trim.
type ResumeResult struct {
	ThreadID       string
	LeadingContext string
	Tokens         int
	Trimmed        []string
}

// errResumeFlagsConflict is --resume-topic and --resume-query together.
var errResumeFlagsConflict = cascade.New(cascade.KindInvalidInput,
	"cascade chat: --resume-topic and --resume-query name different resume sources; pass at most one")

// errChatPrivacyOnNamedThread is a privacy flag combined with --thread on
// a path that would otherwise drop it (a resume, or TUI mode): the named
// thread's tier was fixed when it was created, so the flag is refused
// rather than ignored. (A one-shot turn sends it and the daemon refuses.)
var errChatPrivacyOnNamedThread = cascade.New(cascade.KindInvalidInput,
	"cascade chat: --private/--local-only choose the tier of a NEW session thread; with --thread the thread's "+
		"tier is already fixed. Drop --thread to start a new thread at that tier, or drop the flag")

// refusePrivacyOnNamedThread returns errChatPrivacyOnNamedThread when an
// operator-named thread is combined with a privacy flag.
func refusePrivacyOnNamedThread(opts chatOptions) error {
	tier, err := resolveChatPrivacy(opts)
	if err != nil {
		return err
	}
	if opts.thread != "" && !opts.threadMinted && tier != "" {
		return errChatPrivacyOnNamedThread
	}
	return nil
}

// validateResumeFlags enforces "at most one of --resume-topic/--resume-
// query"; neither set is ordinary chat.
func validateResumeFlags(opts chatOptions) error {
	if opts.resumeTopic != "" && opts.resumeQuery != "" {
		return errResumeFlagsConflict
	}
	return nil
}

// resumeCwd returns this process's working directory, best-effort: an
// unreadable cwd resolves to the general scope server-side (R-16.3), never
// a refusal here.
func resumeCwd() string {
	cwd, _ := os.Getwd()
	return cwd
}

// applyChatResume runs --resume-topic/--resume-query (a no-op without
// them): it creates the session thread when no --thread was named, resumes
// INTO that thread, and stages the block for the next append. opts.thread
// becomes the session thread; an operator-named thread is never replaced.
func applyChatResume(ctx context.Context, cc *cobra.Command, opts *chatOptions) error {
	if opts.resumeTopic == "" && opts.resumeQuery == "" {
		return nil
	}
	if err := refusePrivacyOnNamedThread(*opts); err != nil {
		return err
	}
	tier, err := resolveChatPrivacy(*opts)
	if err != nil {
		return err
	}
	session := newChatSession(opts.thread, tier)
	res, err := runResumeAndPrint(ctx, cc, activeClient(), session,
		ResumeRequest{TopicID: opts.resumeTopic, Query: opts.resumeQuery, Cwd: resumeCwd()}, opts.json)
	opts.thread, opts.threadMinted = session.snapshot()
	if err != nil {
		return err
	}
	opts.leadingContext = res.LeadingContext
	return nil
}

// runResumeAndPrint resumes req into session and renders the result to
// cc's own streams.
func runResumeAndPrint(ctx context.Context, cc *cobra.Command, client Client, session *chatSession,
	req ResumeRequest, asJSON bool) (ResumeResult, error) {
	res, err := resumeIntoSession(ctx, client, session, req)
	if err != nil {
		return ResumeResult{}, err
	}
	if err := writeResumeResult(cc, res, asJSON); err != nil {
		return ResumeResult{}, err
	}
	return res, nil
}

// resumeLeadingContextKey carries a block resolved before TUI mode was
// entered to the TUI's first turn.
type resumeLeadingContextKey struct{}

// withResumeLeadingContext returns ctx carrying s; "" is a no-op.
func withResumeLeadingContext(ctx context.Context, s string) context.Context {
	if s == "" {
		return ctx
	}
	return context.WithValue(ctx, resumeLeadingContextKey{}, s)
}

// resumeLeadingContextFrom reads back what withResumeLeadingContext stored.
func resumeLeadingContextFrom(ctx context.Context) string {
	s, _ := ctx.Value(resumeLeadingContextKey{}).(string)
	return s
}

// writeResumeResult renders res to cc.OutOrStdout() (--json: the wire
// field names; otherwise a header plus the block) and each trim report
// line to cc.ErrOrStderr() as "warning: <line>" -- never a bare os.Stdout
// (internal/build/outputgate.go's boundary).
func writeResumeResult(cc *cobra.Command, res ResumeResult, asJSON bool) error {
	if asJSON {
		trimmed := res.Trimmed
		if trimmed == nil {
			trimmed = []string{}
		}
		return json.NewEncoder(cc.OutOrStdout()).Encode(struct {
			ThreadID       string   `json:"thread_id"`
			LeadingContext string   `json:"leading_context"`
			Tokens         int      `json:"tokens"`
			Trimmed        []string `json:"trimmed"`
		}{res.ThreadID, res.LeadingContext, res.Tokens, trimmed})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resumed context (%d tokens):\n%s\n", res.Tokens, res.LeadingContext)
	if _, err := cc.OutOrStdout().Write([]byte(b.String())); err != nil {
		return err
	}
	for _, line := range res.Trimmed {
		if _, err := fmt.Fprintf(cc.ErrOrStderr(), "warning: %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// parseResumeCommand recognizes "/resume topic <id>" and "/resume query
// <terms...>". ok is false for anything else, which the caller reports as
// a usage error or sends as an ordinary prompt.
func parseResumeCommand(input string) (req ResumeRequest, ok bool) {
	fields := strings.Fields(input)
	if len(fields) < 3 || fields[0] != "/resume" {
		return ResumeRequest{}, false
	}
	switch fields[1] {
	case "topic":
		return ResumeRequest{TopicID: fields[2]}, true
	case "query":
		return ResumeRequest{Query: strings.Join(fields[2:], " ")}, true
	default:
		return ResumeRequest{}, false
	}
}
