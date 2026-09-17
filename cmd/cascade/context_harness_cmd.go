// Purpose: `cascade context harness list|sync` (P1-E16-W4-S35-T3) — the
//
//	surface 07's command tree folds harness detection and instruction
//	sync under.
//
// Inputs: the shared contextScopeDeps.
// Outputs: a harness table (list), or the same report `context sync`
//
//	prints (sync).
//
// Constraints: `harness sync` IS `context sync`. It calls the same
//
//	fetchContextSync and the same renderer, so the two spellings cannot
//	drift apart — 07 names the harness form, and the older one stays
//	because scripts and this tree's own comments use it.
//
// SPORT: cmd/cascade/context-harness (ADD) — P1-E16-W4-S35-T3.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newContextHarnessCmd builds the `context harness` group.
func newContextHarnessCmd(deps contextScopeDeps) *cobra.Command {
	harness := &cobra.Command{
		Use:   "harness",
		Short: "Coding-harness detection and instruction sync",
	}
	harness.AddCommand(newContextHarnessListCmd(deps))
	harness.AddCommand(newContextHarnessSyncCmd(deps))
	return harness
}

// newContextHarnessListCmd builds `context harness list`.
func newContextHarnessListCmd(deps contextScopeDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every supported harness, whether it is installed, and whether its instructions have drifted",
		Long: "Reports all three supported harnesses every time, installed or not.\n\n" +
			"A harness that is absent is listed as absent rather than omitted:\n" +
			"\"not installed\" and \"this build forgot about it\" are different\n" +
			"answers and a list that omitted one could not tell them apart.\n\n" +
			"Drift is reported only for a harness that IS installed. Cascade\n" +
			"generates instruction files for all three regardless of what is on\n" +
			"the machine, so drift on an absent harness is true of the file and\n" +
			"useless to the reader.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := fetchContextHarnessList(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(contextHarnessHumanView{result})
		},
	}
}

// newContextHarnessSyncCmd builds `context harness sync`, 07's spelling
// of `context sync`.
func newContextHarnessSyncCmd(deps contextScopeDeps) *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Check or regenerate harness instruction files against the current context",
		Long: "The same operation as `cascade context sync`, under the name 07's\n" +
			"command tree gives it. Both call one implementation, so they cannot\n" +
			"report differently.\n\n" +
			"--check reports drift without writing anything and exits non-zero\n" +
			"if any file is stale. Without it, every stale file is regenerated\n" +
			"atomically; a second run over an already-synced tree writes nothing\n" +
			"and exits 0.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := fetchContextSync(cmd.Context(), deps, checkOnly)
			if err != nil {
				return err
			}
			if err := contextSyncRender(cmd, result, checkOnly); err != nil {
				return err
			}
			return contextSyncOutcomeError(result, checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false,
		"report drift without writing; exit non-zero if any file is stale")
	return cmd
}

// fetchContextHarnessList routes through the daemon when one is live and
// through the embedded runtime otherwise, the same rule every other
// context verb follows.
func fetchContextHarnessList(ctx context.Context, deps contextScopeDeps) (daemon.ContextHarnessListResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return daemon.ContextHarnessListResult{}, cascade.Wrap(cascade.KindUnavailable, err,
			"cascade context harness list: resolve cwd")
	}
	params := daemon.ContextHarnessListParams{Cwd: cwd}

	if st, ok := runtime.DaemonlessStateFrom(ctx); ok && !st.Embedded {
		settings, serr := daemon.ResolveSettings(nil, deps.Paths)
		if serr != nil {
			return daemon.ContextHarnessListResult{}, serr
		}
		c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextAssembleDialTimeout)
		var result daemon.ContextHarnessListResult
		if derr := c.Do(ctx, daemon.ContextHarnessListMethod, params, &result); derr != nil {
			return daemon.ContextHarnessListResult{}, derr
		}
		return result, nil
	}
	return daemon.ComputeContextHarnessList(ctx, params)
}

// contextHarnessHumanView wraps daemon.ContextHarnessListResult for TTY
// rendering, one row per harness.
//
// The result is EMBEDDED rather than held in a named field, and the
// renderer is String rather than a method of this file's own invention:
// output.Writer.Result marshals whatever it is handed for --json and
// falls back to fmt's default verb for a value that is not a
// fmt.Stringer. A view with an unexported field and a differently-named
// renderer therefore prints a Go struct dump on a terminal and an empty
// object under --json, with nothing failing anywhere. Every other view in
// this package is shaped this way; this one now is too.
type contextHarnessHumanView struct {
	daemon.ContextHarnessListResult
}

// String renders one row per harness, in the detector's fixed order.
func (v contextHarnessHumanView) String() string {
	if len(v.Harnesses) == 0 {
		return "no harnesses were reported"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-10s  %-13s  %s\n", "HARNESS", "STATUS", "DETAIL")
	for _, h := range v.Harnesses {
		fmt.Fprintf(&b, "%-10s  %-13s  %s\n", h.Kind, harnessStatus(h), harnessDetail(h))
	}
	return strings.TrimRight(b.String(), "\n")
}

// harnessStatus names the row's state in the vocabulary an operator acts
// on, rather than as a pair of booleans they have to decode.
func harnessStatus(h cascadecontext.HarnessState) string {
	switch {
	case !h.Detected:
		return "not installed"
	case h.Drift:
		return "drifted"
	default:
		return "in sync"
	}
}

// harnessDetail is the second column: why it drifted, or where we looked.
func harnessDetail(h cascadecontext.HarnessState) string {
	if h.Drift && h.DriftReason != "" {
		if h.InstructionPath != "" {
			return h.InstructionPath + ": " + h.DriftReason
		}
		return h.DriftReason
	}
	return h.InstallPath
}

// harnessSummaryLine is the one-line summary the doctor row and the init
// wizard both want: the installed kinds, or a statement that there are
// none.
func harnessSummaryLine(states []cascadecontext.HarnessState) string {
	detected := cascadecontext.DetectedKinds(states)
	if len(detected) == 0 {
		return "no supported harness is installed"
	}
	names := make([]string, 0, len(detected))
	for _, kind := range detected {
		names = append(names, string(kind))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
