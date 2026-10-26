package cmd

// Purpose (this file): runChat's dispatch helpers -- the no-prompt early
//   refusals, the one-shot dispatch, the TUI context and result rendering
//   -- split from chat.go under Art.10.3's 300-line cap.
// SPORT: plugins/cascade-pa:cmd:chat (CHANGED) -- P1-E22-W5-S47-T2.

import (
	"context"
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"strings"

	"github.com/spf13/cobra"
)

// dispatchOneShot is runChat's one-shot dispatch. The flag tier rides on
// the request only when no thread exists yet (the daemon mints and marks
// it in one transaction); a thread this session created already carries
// its marker, and an operator-named thread with a flag is sent as-is so
// the daemon refuses it (never silently ignored).
func dispatchOneShot(ctx context.Context, cc *cobra.Command, opts chatOptions) error {
	privacy, err := resolveChatPrivacy(opts)
	if err != nil {
		return err
	}
	if opts.threadMinted {
		privacy = ""
	}
	res, err := activeClient().OneShot(ctx, OneShotRequest{
		Thread: opts.thread, Prompt: opts.prompt, Privacy: privacy, LeadingContext: opts.leadingContext,
	})
	if err != nil {
		return err
	}
	return writeOneShotResult(cc, res, opts)
}

// refuseNoPromptFreshSession is runChat's early exit for a session with no
// prompt and no operator-named --thread to open: CASCADE_NO_INPUT=1
// refuses (automation parity), and so does Windows tier-2 (no TUI). Both
// run before applyChatResume, so neither case ever creates a thread.
func refuseNoPromptFreshSession(opts chatOptions, lookupEnv envLookup) error {
	if opts.prompt != "" || opts.thread != "" {
		return nil
	}
	if v, ok := lookupEnv("CASCADE_NO_INPUT"); ok && v == "1" {
		return errChatNoInputNoPrompt
	}
	if goruntime.GOOS == "windows" {
		return errChatWindowsTUIRefusal
	}
	return nil
}

// tuiContext carries what runChat resolved into TUI mode through ctx
// (runTUI's signature stays unchanged): a resume's staged block for the
// first turn, and the flag tier for a /resume or first turn that creates
// the session thread. With a thread already present the tier is unused
// (refusePrivacyOnNamedThread has already refused a flag on a named one).
func tuiContext(ctx context.Context, opts chatOptions) context.Context {
	ctx = withResumeLeadingContext(ctx, opts.leadingContext)
	tier, err := resolveChatPrivacy(opts)
	if err != nil {
		return ctx
	}
	return withChatSessionTier(ctx, tier)
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
