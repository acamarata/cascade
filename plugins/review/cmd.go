// Purpose (this file): the `review` cobra.Command (P1-E25-W5-S52-T5): the
//
//	CLI surface for the native adversarial ReviewProvider T4 built and
//	wired via internal/plugins/review_wiring.go's init(). NewReviewCommand
//	builds a real, standalone command — RunCommand (plugin.go) executes
//	the SAME command against a builtin-dispatch's raw args, matching
//	plugins/cascade-pa/commands.go's handlers.RunCommand precedent
//	(pacmd.NewChatCommand(); c.SetArgs(args); c.ExecuteContext(ctx)) —
//	never a second, divergent construction.
//
// MOUNT (resolved — T0 decision D1, 2026-09-21, closing the adversarial
// review's BLOCK 1). `cascade review` IS a top-level noun on the shipped
// binary's cobra root: cmd/cascade/review_mount.go's mountReviewCmd calls
// review.NewReviewCommand() (this file) directly and root.AddCommand(cmd),
// called from root.go's mountSubcommands next to mountChatCmd — the same
// direct-mount shape 07-CLI-COMMAND-TREE's plugin-namespace note and
// R-16.58's literal "mounts `cascade review`" require (never
// mountPluginNamespaceCmds' generic per-manifest-id nesting, which would
// spell the command `cascade cascade-review review` instead). The prior
// build's "COMPOSITION-ROOT DEVIATION" note (this ticket's files_scope
// being plugins/review/** only, with cmd/cascade under concurrent edit)
// is superseded: D1 is a pre-authorized, recorded scope deviation adding
// exactly the one new cmd/cascade file and the one root.go call site that
// note said were missing — see review_mount.go's own header.
//
// Inputs: --diff (file path or "-" for stdin), --pr (a PR reference
//
//	string of the form "owner/repo#number"; real format validation, but
//	the fetch itself refuses — see runReview's own doc comment), --level
//	(A/B/C, default B), --json.
//
// Outputs: TTY mode prints a ranked finding list to stdout; --json prints
//
//	the versioned envelope below to stdout. Diagnostics (the daemon-not-
//	running warning path, none in this command) would go to stderr, never
//	mixed with stdout=data (D/S-06.T5). A provider/egress refusal (an
//	invalid level, a missing --diff/--pr, an unattributable diff, a
//	sensitivity refusal from the router, a daemon-unreachable dial
//	failure) is returned as a typed pkg/cascade error and propagates to a
//	non-zero process exit via cascade.ExitCode — this file never
//	swallows one.
//
// Constraints: plugins/review may import pkg/** only, never internal/**
//
//	(Art.10.2, plugins-providers-boundary depguard rule). No interactive
//	prompt exists anywhere in this command (every input is a flag), so
//	CASCADE_NO_INPUT=1 changes nothing about its behavior — the
//	automation-parity criterion (06-FORGE-SPEC §5.8) is satisfied
//	structurally, matching docs/cli-reference/pa-pair.md's identical
//	"never prompts" precedent. Output rendering (the --json envelope and
//	the human finding list) is split into cmd_render.go, same package,
//	purely to stay under Art.10.3's 300-line/file cap — see that file's
//	own header for why internal/output.Envelope is unreachable here.
//
// SPORT: plugins/review:cmd (ADD) — P1-E25-W5-S52-T5.

package review

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// reviewFlags holds the parsed flag values for one `review` invocation.
type reviewFlags struct {
	diff  string
	pr    string
	level string
	json  bool
}

// NewReviewCommand builds the `review` cobra.Command: the COMMAND FORM per
// 07-CLI-COMMAND-TREE §review (`cascade review [--diff <file|-|pr-ref>]
// [--pr <ref>] [--level A|B|C]`). Every field of flags is a fresh local —
// calling this twice never shares state, matching pacmd.NewChatCommand's
// same per-call-fresh contract (chat.go's own doc comment).
func NewReviewCommand() *cobra.Command {
	flags := &reviewFlags{level: "B"}
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Run a CR-A/CR-B/CR-C adversarial code review over a diff",
		Long: "Runs the native adversarial reviewer (internal/review, via the cascade-review builtin\n" +
			"plugin) over a unified diff, at the requested CR tier. Exactly one of --diff or --pr is\n" +
			"required.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReview(cmd, flags)
		},
	}
	f := cmd.Flags()
	f.StringVar(&flags.diff, "diff", "", "unified diff: a file path, or '-' to read it from stdin")
	f.StringVar(&flags.pr, "pr", "", "a pull request reference, \"owner/repo#number\" (fetch is refused; see runReview's doc comment)")
	f.StringVar(&flags.level, "level", flags.level, "review tier: A (lightweight), B (peer, default), or C (adversarial)")
	f.BoolVar(&flags.json, "json", false, "emit the versioned JSON envelope instead of human-readable text")
	return cmd
}

// runReview is RunE's body: validate flags, resolve the diff, dispatch
// through the reviewProvider seam (plugin.go — the real T4 engine once
// review_wiring.go's init() has run, or the honest unwiredReviewProvider
// refusal otherwise; never a second, stub construction), and render the
// result or propagate the error unchanged.
//
// --pr's format is validated for real (parsePRRef); the fetch itself
// refuses (errPRFetchUnsupported's own doc comment — T0 decision D6,
// 2026-09-21). Every refusal is typed, never a silent no-op or a
// fabricated resolution, matching docs/cli-reference/pa-pair.md's own
// subject-refusal precedent.
func runReview(cmd *cobra.Command, flags *reviewFlags) error {
	if err := validateReviewFlags(flags); err != nil {
		return err
	}
	if flags.pr != "" {
		if _, _, _, err := parsePRRef(flags.pr); err != nil {
			return err
		}
		return errPRFetchUnsupported(flags.pr)
	}
	level, err := parseReviewLevel(flags.level)
	if err != nil {
		return err
	}
	diff, err := readReviewDiff(cmd, flags.diff)
	if err != nil {
		return err
	}
	resp, err := reviewProvider.Review(cmd.Context(), provider.ReviewRequest{Level: level, Diff: diff})
	if err != nil {
		return err
	}
	return renderReviewResponse(cmd, flags.json, resp)
}

// validateReviewFlags enforces the "exactly one of --diff or --pr" no-args
// contract (acceptance criterion: "cascade review with no --diff or --pr
// exits non-zero with a usage message on stderr" — a cobra RunE error
// reaches stderr via the caller's own error rendering, never printed here).
func validateReviewFlags(flags *reviewFlags) error {
	switch {
	case flags.diff == "" && flags.pr == "":
		return cascade.New(cascade.KindInvalidInput, "cascade review: one of --diff or --pr is required")
	case flags.diff != "" && flags.pr != "":
		return cascade.New(cascade.KindInvalidInput, "cascade review: --diff and --pr are mutually exclusive")
	default:
		return nil
	}
}

// parseReviewLevel maps the CLI's bare A/B/C spelling (07-CLI-COMMAND-TREE
// §review; case-insensitive) onto pkg/provider.ReviewCRLevel's "CR-A"/
// "CR-B"/"CR-C" wire values. An unrecognized value is a typed usage error,
// never silently defaulted.
func parseReviewLevel(raw string) (provider.ReviewCRLevel, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "A":
		return provider.ReviewCRLevelA, nil
	case "B":
		return provider.ReviewCRLevelB, nil
	case "C":
		return provider.ReviewCRLevelC, nil
	default:
		return "", cascade.Newf(cascade.KindInvalidInput,
			"cascade review: invalid --level %q: must be A, B, or C", raw)
	}
}

// parsePRRef validates --pr's required "owner/repo#number" shape — real,
// useful validation independent of whether this command can go on to
// actually fetch the diff (errPRFetchUnsupported below). A malformed ref
// is refused before any other work, exactly like an invalid --level.
func parsePRRef(raw string) (owner, repo string, number int, err error) {
	malformed := cascade.Newf(cascade.KindInvalidInput, "cascade review: --pr must be \"owner/repo#number\", got %q", raw)
	before, after, ok := strings.Cut(raw, "#")
	if !ok {
		return "", "", 0, malformed
	}
	owner, repo, ok = strings.Cut(before, "/")
	if !ok || owner == "" || repo == "" {
		return "", "", 0, malformed
	}
	number, convErr := strconv.Atoi(after)
	if convErr != nil || number <= 0 {
		return "", "", 0, malformed
	}
	return owner, repo, number, nil
}

// errPRFetchUnsupported is --pr's real, typed refusal (T0 decision D6,
// 2026-09-21, superseding the ticket's own "no network call in this
// ticket" framing). Fetching a PR's unified diff needs two things this
// build does not have, both outside this ticket's files_scope/D7
// deny-list: (1) a host bridge — plugins/review may import pkg/** only,
// never internal/** (Art.10.2), so reaching internal/ci from here is
// architecturally impossible without a review_wiring.go-shaped bridge
// (internal/plugins is the one package allowed to import both sides; none
// exists for this yet); (2) even from that bridge, internal/ci's only
// egress class is "ci-poll" (internal/hooks/egress/classes.go), scoped to
// Actions run/job polling (R-21.265/06-§5.17) — fetching a PR's unified
// diff is a different purpose with different sensitivity (full source
// content vs. pass/fail metadata) and has no egress class of its own.
// Adding either is real work for a ticket that owns internal/ci and
// internal/hooks/egress, not this one (internal/ci/** is under
// concurrent edit by another lane this phase — git status, verified
// before writing this file).
func errPRFetchUnsupported(ref string) error {
	return cascade.Newf(cascade.KindUnsupported,
		"cascade review: --pr %q is a well-formed PR reference, but this build refuses to fetch its diff: "+
			"plugins/review cannot reach internal/ci's GitHub client (Art.10.2 boundary: no host bridge exists "+
			"yet, matching internal/plugins/review_wiring.go's pattern for the review engine itself), and "+
			"internal/ci's only egress class (\"ci-poll\") is scoped to Actions run/job polling, not PR-diff "+
			"fetching; resolve the pull request to a diff yourself (e.g. `git diff` against its base) and pass "+
			"it via --diff", ref)
}

// readReviewDiff resolves --diff's value: "-" reads cmd.InOrStdin() (cobra's
// own stdin seam, matching printInChatReply's cmd.OutOrStdout() precedent
// in plugins/cascade-pa/commands.go — never a bare os.Stdin reference), any
// other value is a file path read via os.ReadFile. A read failure is a
// typed KindUnavailable/KindInvalidInput error, never an empty diff passed
// through silently.
func readReviewDiff(cmd *cobra.Command, path string) (string, error) {
	if path == "-" {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", cascade.Wrap(cascade.KindUnavailable, err, "cascade review: read --diff from stdin")
		}
		return string(b), nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // an operator-supplied CLI flag, not attacker-controlled input
	if err != nil {
		return "", cascade.Wrapf(cascade.KindInvalidInput, err, "cascade review: read --diff file %q", path)
	}
	return string(b), nil
}
