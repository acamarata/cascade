// Purpose: `cascade memory review` (07-CLI-COMMAND-TREE §memory review —
//
//	the promotion queue) — the surface where a human sees what the
//	mechanical promotion lane decided and acts on it. This file is the
//	cobra half: the verb, its flag surface, and the resolution of the one
//	action a non-interactive invocation carries out. The rendering lives
//	in memory_review_view.go.
//
// Inputs: cobra args/flags, the CASCADE_MEMORY_REVIEW_ACTION environment
//
//	variable read through the injected memoryDeps.Getenv, and a memoryDeps
//	injected at construction so no test touches a real socket (Art.7.1).
//
// Outputs: the review listing or the result of one action, through
//
//	internal/output only; a typed taxonomy error, scrubbed by memoryCall.
//
// Constraints: LISTING NEVER ACTS. With no address argument the command
//
//	only reads, and an action flag with no address is refused rather than
//	applied to the whole queue: a review surface that could act on
//	everything a listing happened to contain would be an automated lane
//	wearing a human lane's name. Non-interactive throughout (§5.8):
//	nothing prompts, so the action flags and the env var are the entire
//	way an action is chosen. No platform-specific imports (Art.5).
//
// SPORT: cmd.cascade.cmd.memory.review (ADD, P1-E07-W2-S14-T3).
package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory/review"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memoryReviewActionEnv is the 08 §2 environment variable that selects an
// action when no flag does. It exists so an automated caller that cannot
// pass flags (a wrapper, a job definition) can still name its action; it
// never selects an action on its own, because an address is still
// required.
const memoryReviewActionEnv = "CASCADE_MEMORY_REVIEW_ACTION"

// memoryReviewFlags holds the action-selecting flags of `memory review`.
type memoryReviewFlags struct {
	section     string
	autoApprove bool
	autoSkip    bool
	revert      bool
	deferDays   int
}

// newMemoryReviewCmd builds `cascade memory review`.
func newMemoryReviewCmd(deps memoryDeps) *cobra.Command {
	var f memoryReviewFlags
	cmd := &cobra.Command{
		Use:   "review [<kind>/<name>]",
		Short: "Review candidate memories the promotion ladder has not promoted",
		Long: "Show the promotion queue, or act on ONE candidate in it.\n\n" +
			"With no address, this only reads. It lists two things: candidates\n" +
			"still BELOW the mechanical promotion threshold, and promotions\n" +
			"that already happened and are still standing. Neither is a\n" +
			"recommendation — the thresholds are printed beside the counts so\n" +
			"you can check the claim rather than take it — and listing changes\n" +
			"nothing whatsoever in the store.\n\n" +
			"With an address and one action flag, it carries out exactly that\n" +
			"action on exactly that candidate:\n" +
			"  --auto-approve   promote it now, ahead of the threshold\n" +
			"  --auto-skip      leave it as it is (recorded, changes nothing)\n" +
			"  --defer-days N   hide it from the queue for N days\n" +
			"  --revert         take back a promotion\n\n" +
			"Nothing prompts (" + memoryReviewActionEnv + " selects the action\n" +
			"when no flag does). An action flag with no address is refused:\n" +
			"there is no bulk mode, by design.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runMemoryReviewList(cmd, deps, f)
			}
			return runMemoryReviewAct(cmd, deps, f, args[0])
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&f.section, "section", "",
		"restrict the listing to one section: pending|promoted")
	flags.BoolVar(&f.autoApprove, "auto-approve", false,
		"action: promote the addressed candidate ahead of the threshold")
	flags.BoolVar(&f.autoSkip, "auto-skip", false,
		"action: leave the addressed candidate exactly as it is")
	flags.BoolVar(&f.revert, "revert", false,
		"action: take back the addressed candidate's promotion")
	flags.IntVar(&f.deferDays, "defer-days", 0,
		"action: hide the addressed candidate from the queue for N days")
	return cmd
}

// runMemoryReviewList performs the read path.
func runMemoryReviewList(cmd *cobra.Command, deps memoryDeps, f memoryReviewFlags) error {
	if action, err := memoryReviewAction(deps, f); err != nil {
		return scrubDiagnostic(err)
	} else if action != "" {
		return scrubDiagnostic(cascade.Newf(cascade.KindInvalidInput,
			"cascade memory review: %q needs the <kind>/<name> address of the "+
				"candidate to act on; there is no bulk mode", action))
	}
	var result review.ListResult
	params := review.ListParams{Section: f.section}
	if err := memoryCall(cmd, deps, review.MethodReviewList, params, &result); err != nil {
		return err
	}
	return memoryWriter(cmd).Result(reviewListView{result})
}

// runMemoryReviewAct performs one addressed action.
func runMemoryReviewAct(
	cmd *cobra.Command, deps memoryDeps, f memoryReviewFlags, id string,
) error {
	action, err := memoryReviewAction(deps, f)
	if err != nil {
		return scrubDiagnostic(err)
	}
	if action == "" {
		return scrubDiagnostic(cascade.Newf(cascade.KindInvalidInput,
			"cascade memory review %s: name an action (--auto-approve, --auto-skip, "+
				"--defer-days N or --revert), or set %s", id, memoryReviewActionEnv))
	}
	params := review.ActParams{ID: id, Action: action, DeferDays: f.deferDays}
	var result review.ActResult
	if err := memoryCall(cmd, deps, review.MethodReviewAct, params, &result); err != nil {
		return err
	}
	return memoryWriter(cmd).Result(reviewActView{result})
}

// memoryReviewAction resolves the single action this invocation selects,
// or "" when it selects none.
//
// Flags win over the environment variable, and two flags are a refusal
// rather than a precedence rule: a caller that asked for both approve and
// revert has not said what it wants, and picking one for it would carry
// out a decision nobody made.
func memoryReviewAction(deps memoryDeps, f memoryReviewFlags) (string, error) {
	var chosen []string
	for _, c := range []struct {
		on     bool
		action string
	}{
		{f.autoApprove, review.ActionApprove},
		{f.autoSkip, review.ActionSkip},
		{f.deferDays != 0, review.ActionDefer},
		{f.revert, review.ActionRevert},
	} {
		if c.on {
			chosen = append(chosen, c.action)
		}
	}
	if len(chosen) > 1 {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"cascade memory review: %v were all requested; name exactly one action", chosen)
	}
	if len(chosen) == 1 {
		return chosen[0], nil
	}
	return memoryReviewEnvAction(deps)
}

// memoryReviewEnvAction reads the action from the environment, refusing a
// value outside the four names rather than falling back to a listing: a
// caller that set the variable meant to act, and silently reading instead
// would report success for an action that never happened.
func memoryReviewEnvAction(deps memoryDeps) (string, error) {
	if deps.Getenv == nil {
		return "", nil
	}
	switch v := deps.Getenv(memoryReviewActionEnv); v {
	case "":
		return "", nil
	case review.ActionApprove, review.ActionSkip, review.ActionDefer, review.ActionRevert:
		return v, nil
	default:
		return "", cascade.Newf(cascade.KindInvalidInput,
			"%s is %q, want one of %s, %s, %s, %s", memoryReviewActionEnv, v,
			review.ActionApprove, review.ActionSkip, review.ActionDefer, review.ActionRevert)
	}
}
