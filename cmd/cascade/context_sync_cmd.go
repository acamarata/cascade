// Purpose: `cascade context sync [--check]` (04-PEWS-PLAN-W1-W3.md §Wave 2
//
//	Epic E S-09 T4). Mounted under the existing `context` group
//	(context_scope.go's newContextCmd, alongside slice/show), in its own
//	file rather than context_cmd.go: adding it there would carry that
//	file past the repo's 300-line cap, the same reason
//	daemon_unix_run.go's own doc comment gives for splitting
//	context_assemble.go's registration out of context_scope.go's —
//	CONTRADICTION against this ticket's own files_scope, which names
//	context_cmd.go as the file to change; recorded in this ticket's
//	journal.
//
// Inputs: a --check flag plus the same contextScopeDeps every context_*.go
//
//	file shares.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure — --check returns KindConflict when any file is
//	stale (after printing the report), matching WriteHarnessFile's own
//	handEditRefusal use of KindConflict for the same "on-disk state
//	disagrees with what cascade would produce" condition.
//
// Constraints: never a hand-rolled JSON-RPC request — routes through
//
//	internal/client.Client.Do exactly like fetchContextSlice/
//	fetchContextShow. The embedded fallback calls internal/context.Sync
//	directly, the SAME function internal/daemon/context_sync.go's RPC
//	handler calls, so the two paths can never disagree about what
//	changed.
//
// SPORT: cli/context-sync/ADD (per T-4 sport_updates).
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// contextSyncDialTimeout mirrors contextAssembleDialTimeout's established
// value for this file's one daemon round trip.
const contextSyncDialTimeout = contextAssembleDialTimeout

// newContextSyncCmd builds `context sync [--check]`.
func newContextSyncCmd(deps contextScopeDeps) *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Check or regenerate harness instruction files against the current context",
		Long: "Compares each coding harness's on-disk instruction file against a\n" +
			"fresh generation from the current context cascade.\n\n" +
			"--check reports drift without writing anything and exits non-zero\n" +
			"if any file is stale. The default mode regenerates every stale\n" +
			"file atomically and exits 0, printing how many files changed; a\n" +
			"second run over an already-synced tree changes nothing.",
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

// contextSyncRender writes the human/--json view for result, choosing the
// drift table (checkOnly) or the delta report (regenerate).
func contextSyncRender(cmd *cobra.Command, result daemon.ContextSyncResult, checkOnly bool) error {
	if checkOnly {
		return contextScopeOutputWriter(cmd).Result(contextSyncCheckHumanView{result})
	}
	return contextScopeOutputWriter(cmd).Result(contextSyncHumanView{result})
}

// contextSyncOutcomeError decides the process result. Only --check can
// fail: the default mode's contract is "regenerate and exit 0" (this
// ticket's acceptance criteria), so a stale count there is not a failure —
// it is exactly what regeneration just fixed.
func contextSyncOutcomeError(result daemon.ContextSyncResult, checkOnly bool) error {
	if !checkOnly {
		return nil
	}
	stale := 0
	for _, d := range result.Drift {
		if d.Stale {
			stale++
		}
	}
	if stale == 0 {
		return nil
	}
	return cascade.Newf(cascade.KindConflict,
		"cascade context sync --check: %d file(s) stale", stale)
}

// fetchContextSync routes through the client when the daemonless probe
// confirms a live daemon, and through the embedded internal/context.Sync
// call otherwise, mirroring fetchContextSlice's own routing rule exactly.
func fetchContextSync(ctx context.Context, deps contextScopeDeps, checkOnly bool) (daemon.ContextSyncResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return daemon.ContextSyncResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade context sync: resolve cwd")
	}
	params := daemon.ContextSyncParams{Cwd: cwd, CheckOnly: checkOnly}

	st, ok := runtime.DaemonlessStateFrom(ctx)
	if ok && !st.Embedded {
		settings, err := daemon.ResolveSettings(nil, deps.Paths)
		if err != nil {
			return daemon.ContextSyncResult{}, err
		}
		c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextSyncDialTimeout)
		var result daemon.ContextSyncResult
		if err := c.Do(ctx, daemon.ContextSyncMethod, params, &result); err != nil {
			return daemon.ContextSyncResult{}, err
		}
		return result, nil
	}
	sr, err := cascadecontext.Sync(ctx, cwd, nil, checkOnly)
	result := daemon.ContextSyncResult{
		Drift: sr.Drift, Files: sr.Files, Regenerated: sr.Regenerated, AlreadyFresh: sr.AlreadyFresh,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

// contextSyncCheckHumanView renders --check's drift table.
type contextSyncCheckHumanView struct{ daemon.ContextSyncResult }

// String renders one row per considered file: harness, path, fresh/stale,
// and the reason when stale.
func (v contextSyncCheckHumanView) String() string {
	if len(v.Drift) == 0 {
		return "0 files considered (no context tiers resolved above this directory)"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "HARNESS\tPATH\tSTATUS\tREASON\n")
	for _, d := range v.Drift {
		status := "fresh"
		if d.Stale {
			status = "stale"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", d.Harness, d.Path, status, d.Reason)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// contextSyncHumanView renders the default regenerate mode's delta report.
type contextSyncHumanView struct{ daemon.ContextSyncResult }

// String renders the N-regenerated/M-already-fresh summary this ticket's
// contract names verbatim.
func (v contextSyncHumanView) String() string {
	return fmt.Sprintf("%d file(s) regenerated / %d already fresh", v.Regenerated, v.AlreadyFresh)
}
