// Purpose: `cascade context slice --hook` — the prompt-hydration hook
//
//	(P1-E16-W4-S34-T4, R-16.6a). It reads a harness UserPromptSubmit
//	payload on stdin, assembles a budgeted context slice for that payload's
//	working directory, filters it to the configured fused-score floor, and
//	emits the harness's own context-injection JSON on stdout.
//
// Inputs: the hook payload on stdin; [context.hydration] for the budget,
//
//	the score floor and the timeout.
//
// Outputs: one hookSpecificOutput object, or nothing at all.
// Constraints: hydration is CONTEXT, not policy. There is no failure mode
//
//	in this file that blocks a prompt: every error path prints nothing and
//	exits 0, and the session proceeds unhydrated. The one thing that is
//	never printed is an EMPTY capsule — "no context cleared the bar" and
//	"here is no context" are different statements, and only the first is
//	true.
//
// SPORT: cmd/cascade/context-slice-hook (ADD) — P1-E16-W4-S34-T4.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/pkg/provider"
)

// hookPayload is the harness's UserPromptSubmit stdin object, narrowed to
// the fields this hook reads.
//
// The field names are the harness's own, captured from a real invocation
// (cmd/cascade/testdata/cc-hook-fixtures/userpromptsubmit.json; provenance
// in that directory's README). Unknown fields are IGNORED rather than
// refused: this is an inbound payload from a program that adds fields
// between releases, and a hook that refused a newer harness would take the
// user's context away at exactly the moment they upgraded.
type hookPayload struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
	Prompt        string `json:"prompt"`
}

// hookInjection is the harness's context-injection response object. The
// shape is the harness's own, confirmed end to end by the same capture:
// the probe capsule this object carried reached the model, which read it
// back verbatim.
type hookInjection struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// userPromptSubmitEvent is the only event this hook answers.
const userPromptSubmitEvent = "UserPromptSubmit"

// runContextSliceHook is the --hook branch of `context slice`.
//
// It returns nil on every path. A hook that exits non-zero is one the
// harness reports as an error to the user, and there is no outcome here
// worth interrupting someone's prompt for — not a malformed payload, not
// an unreachable daemon, not a timeout. What each failure DOES produce is
// a degraded event, so the doctor check can say how often hydration is
// silently doing nothing.
func runContextSliceHook(cmd *cobra.Command, deps contextScopeDeps) error {
	payload, ok := decodeHookPayload(cmd.InOrStdin())
	if !ok {
		recordHydrationDegraded(cmd.Context(), deps, "payload_undecodable")
		return nil
	}
	if payload.HookEventName != "" && payload.HookEventName != userPromptSubmitEvent {
		// A hook wired onto the wrong event. Not a degradation: nothing
		// failed, this simply is not our event.
		return nil
	}
	cfg := hydrationSettings(deps)
	ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout)
	defer cancel()

	capsule, ok := buildHydrationCapsule(ctx, deps, payload, cfg)
	if !ok {
		recordHydrationDegraded(cmd.Context(), deps, "slice_failed")
		return nil
	}
	if capsule == "" {
		// Nothing cleared the score floor. A successful no-injection
		// outcome, not a degradation.
		return nil
	}
	return emitHookInjection(cmd.OutOrStdout(), capsule)
}

// decodeHookPayload reads the whole of stdin and decodes it.
func decodeHookPayload(stdin io.Reader) (hookPayload, bool) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxHookPayloadBytes))
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return hookPayload{}, false
	}
	var payload hookPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return hookPayload{}, false
	}
	if payload.Cwd == "" {
		// Without a working directory there is no scope to resolve and
		// no tier to read. Guessing with the hook process's own cwd
		// would hydrate the prompt with some other project's context.
		return hookPayload{}, false
	}
	return payload, true
}

// maxHookPayloadBytes bounds the read. The payload carries the user's whole
// prompt, so the bound is generous; it exists so a harness that streams
// something unexpected cannot exhaust this process's memory.
const maxHookPayloadBytes = 4 << 20

// buildHydrationCapsule assembles the slice and renders the capsule.
//
// The second return distinguishes the two silences: false means the slice
// itself failed (degraded), and true with an empty string means the slice
// succeeded and nothing cleared the floor (not degraded).
func buildHydrationCapsule(ctx context.Context, deps contextScopeDeps, payload hookPayload, cfg hydration.Config) (string, bool) {
	budget := cfg.BudgetTokens
	result, err := fetchContextSliceFor(ctx, deps, daemon.ContextAssembleParams{
		Cwd: payload.Cwd, MaxTokens: &budget,
	})
	if err != nil {
		return "", false
	}
	kept := filterByScore(result.Retrieval, cfg.MinScore)
	if len(kept) == 0 {
		return "", true
	}
	return renderCapsule(hydrationScopeLabel(ctx, deps, payload), kept), true
}

// filterByScore drops every chunk below floor, preserving rank order.
//
// The comparison is >=, so a floor of 0 keeps everything and a chunk
// scoring exactly at the floor is kept: the floor names the lowest
// acceptable score, not the highest rejected one.
func filterByScore(chunks []provider.RetrievedChunk, floor float64) []provider.RetrievedChunk {
	kept := make([]provider.RetrievedChunk, 0, len(chunks))
	for _, c := range chunks {
		if c.Score >= floor {
			kept = append(kept, c)
		}
	}
	return kept
}

// renderCapsule builds the injected text. The first line is the fixed
// R-16.6a header; the body is one line per surviving chunk.
func renderCapsule(scopeLabel string, chunks []provider.RetrievedChunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cascade context (scope: %s, %d items)\n", scopeLabel, len(chunks))
	for _, c := range chunks {
		fmt.Fprintf(&b, "\n- %s", c.Path)
	}
	return b.String()
}

// emitHookInjection writes the harness's injection object to out.
func emitHookInjection(out io.Writer, capsule string) error {
	encoded, err := json.Marshal(hookInjection{HookSpecificOutput: hookSpecificOutput{
		HookEventName: userPromptSubmitEvent, AdditionalContext: capsule,
	}})
	if err != nil {
		// Unreachable for a struct of two strings, and still not worth
		// failing a prompt over.
		return nil
	}
	_, _ = fmt.Fprintln(out, string(encoded))
	return nil
}
