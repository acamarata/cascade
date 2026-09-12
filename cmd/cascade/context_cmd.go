// Purpose: `cascade context slice` and `cascade context show`
//
//	(04-PEWS-PLAN-W1-W3.md §Wave 2 Epic E S-09 T2). Two read-only
//	subcommands mounted under the existing `context` group
//	(context_scope.go's newContextCmd): slice calls the S-09.T1 assembler
//	(internal/context.Assemble) for a token-budgeted context; show
//	renders the S-08 discover+merge pipeline's tiers in full, for human
//	inspection. Routes through the D/S-07.T3 client SDK when the daemon
//	is present, and through the D/S-07.T4 embedded runtime otherwise —
//	the same daemonless auto-fallback context_scope.go's commands use.
//
// Inputs: cobra flags (--budget for slice only) plus the same
//
//	contextScopeDeps context_scope.go already injects, reused here
//	rather than declaring a second, identical deps struct.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure. Read-only — never prompts.
//
// Constraints: never a hand-rolled JSON-RPC request — both fetch
//
//	functions route through internal/client.Client.Do, the one mechanism
//	every typed client wrapper is built on (internal/client/client.go's
//	own doc comment). No new domain logic: internal/context.Assemble,
//	Discover and MergeTiers already ship from S-08/S-09.T1; this file is
//	the CLI surface and output-formatting layer only, and the actual
//	discover/merge/assemble glue lives in internal/daemon/context_assemble.go
//	so the daemon RPC handler and this file's embedded fallback share one
//	implementation rather than two independently-maintained copies.
//
// SPORT: cli/context-slice-show/ADD (per T-2 sport_updates).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// contextAssembleDialTimeout mirrors contextScopeDialTimeout's established
// value for this file's two additional daemon round trips.
const contextAssembleDialTimeout = 5 * time.Second

// newContextSliceCmd builds `context slice`.
func newContextSliceCmd(deps contextScopeDeps) *cobra.Command {
	var budget int
	cmd := &cobra.Command{
		Use:   "slice",
		Short: "Assemble the token-budgeted context slice for this working directory",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var override *int
			if cmd.Flags().Changed("budget") {
				override = &budget
			}
			result, err := fetchContextSlice(cmd.Context(), deps, override)
			if err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(contextSliceHumanView{result})
		},
	}
	cmd.Flags().IntVar(&budget, "budget", 0, "override the max-token budget (must be positive)")
	return cmd
}

// newContextShowCmd builds `context show`.
func newContextShowCmd(deps contextScopeDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Render every resolved context tier in full, for inspection",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := fetchContextShow(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(contextShowHumanView(result))
		},
	}
	return cmd
}

// fetchContextSlice routes through the client when the daemonless probe
// confirms a live daemon, and through the embedded runtime otherwise,
// mirroring fetchContextScopeShow's own routing rule exactly.
func fetchContextSlice(ctx context.Context, deps contextScopeDeps, budget *int) (daemon.ContextSliceResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return daemon.ContextSliceResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade context slice: resolve cwd")
	}
	params := daemon.ContextAssembleParams{Cwd: cwd, MaxTokens: budget}

	st, ok := runtime.DaemonlessStateFrom(ctx)
	if ok && !st.Embedded {
		settings, err := daemon.ResolveSettings(nil, deps.Paths)
		if err != nil {
			return daemon.ContextSliceResult{}, err
		}
		c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextAssembleDialTimeout)
		var result daemon.ContextSliceResult
		if err := c.Do(ctx, daemon.ContextSliceMethod, params, &result); err != nil {
			return daemon.ContextSliceResult{}, err
		}
		return result, nil
	}
	return resolveContextSliceEmbedded(ctx, deps, params)
}

// fetchContextShow routes the same way. context.show needs no store at
// all (ComputeContextShow takes a bare cwd), so its embedded path never
// opens a database.
func fetchContextShow(ctx context.Context, deps contextScopeDeps) (daemon.ContextShowResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return daemon.ContextShowResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade context show: resolve cwd")
	}
	params := daemon.ContextAssembleParams{Cwd: cwd}

	st, ok := runtime.DaemonlessStateFrom(ctx)
	if ok && !st.Embedded {
		settings, err := daemon.ResolveSettings(nil, deps.Paths)
		if err != nil {
			return daemon.ContextShowResult{}, err
		}
		c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextAssembleDialTimeout)
		var result daemon.ContextShowResult
		if err := c.Do(ctx, daemon.ContextShowMethod, params, &result); err != nil {
			return daemon.ContextShowResult{}, err
		}
		return result, nil
	}
	return daemon.ComputeContextShow(ctx, cwd)
}

// resolveContextSliceEmbedded is the D/S-07.T4 embedded runtime path for
// `context slice`: open cascade.db directly (matching
// resolveContextScopeEmbedded's own dbPath convention), apply the scope
// schema idempotently, and call the SAME daemon.ComputeContextSlice the RPC
// handler calls (internal/daemon/context_assemble.go), so the embedded and
// daemon paths can never disagree about what slicing means.
func resolveContextSliceEmbedded(ctx context.Context, deps contextScopeDeps, params daemon.ContextAssembleParams) (daemon.ContextSliceResult, error) {
	dataDir := deps.Paths.DataDir()
	// sql.Open("sqlite", ...) does NOT create dataDir (same gap as
	// node_serve_presence.go's startPresenceSubsystems and
	// context_scope.go's resolveContextScopeEmbedded): the driver opens
	// the file directly and SQLITE_CANTOPENs (error 14) when the parent is
	// missing. On a virgin HOME with no daemon ever run, `context slice`
	// was the first command to hit this, since `doctor` bootstraps the
	// directory itself but nothing forces a user to run it first
	// (DEFECT-context-slice-no-mkdir-virgin-home.md). 0o700 matches
	// openRuntimeStore's (daemon_unix_store.go) own choice for this dir.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return daemon.ContextSliceResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade context slice: create data directory")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return daemon.ContextSliceResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade context slice: open cascade.db")
	}
	defer func() { _ = db.Close() }()

	if err := scope.ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, runtime.NewSystemClock(), dbPath, filepath.Join(dataDir, "backups")); err != nil {
		return daemon.ContextSliceResult{}, err
	}
	deps2 := scope.ResolveDeps{Store: scope.NewGraphStore(db), GitRoot: cliGitRoot}
	return daemon.ComputeContextSlice(ctx, deps2, params)
}

// contextSliceHumanView wraps daemon.ContextSliceResult for TTY rendering:
// one line per slot with its token count and item count.
type contextSliceHumanView struct {
	daemon.ContextSliceResult
}

// String renders one row per slot (tier/retrieval/memory), each with its
// token count and source count, plus a totals row and any dropped items.
func (v contextSliceHumanView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "SLOT\tTOKENS\tSOURCES\n")
	_, _ = fmt.Fprintf(tw, "tier\t%d\t%d\n", v.Counts.Tier, len(v.Tier))
	_, _ = fmt.Fprintf(tw, "retrieval\t%d\t%d\n", v.Counts.Retrieval, len(v.Retrieval))
	_, _ = fmt.Fprintf(tw, "memory\t%d\t%d\n", v.Counts.Memory, len(v.Memory))
	_, _ = fmt.Fprintf(tw, "total\t%d\t%d\n", v.Counts.Total, len(v.Tier)+len(v.Retrieval)+len(v.Memory))
	for _, d := range v.Dropped {
		_, _ = fmt.Fprintf(tw, "dropped\t%s\t%s\n", d.Slot, d.Reason)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// contextShowHumanView wraps daemon.ContextShowResult for TTY rendering: a
// bordered block per tier.
type contextShowHumanView daemon.ContextShowResult

// String renders each tier as a header line (name and token count)
// followed by its content, tiers separated by a blank line.
func (v contextShowHumanView) String() string {
	if len(v.Tiers) == 0 {
		return "(no context tiers resolved)"
	}
	var buf strings.Builder
	for i, t := range v.Tiers {
		if i > 0 {
			buf.WriteString("\n\n")
		}
		header := fmt.Sprintf("== %s (%d tokens) ==", t.Name, t.Tokens)
		buf.WriteString(header)
		buf.WriteString("\n")
		buf.WriteString(t.Content)
	}
	return buf.String()
}
