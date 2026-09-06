// Purpose: `cascade context scope show [--json]` (07-CLI-COMMAND-TREE
//
//	§Round-16 additions, R-16.21). Routes through the D/S-07.T3 client SDK
//	when the daemon is present, and through the D/S-07.T4 embedded runtime
//	(a directly-opened cascade.db) otherwise — the daemonless auto-fallback
//	root.go's PersistentPreRunE already attached to every command's
//	context. Windows tier-2 executes the embedded path unconditionally
//	(no daemon exists there at all), which is why this file — unlike
//	daemon_unix_store.go — carries no `!windows` build tag and blank-
//	imports modernc.org/sqlite itself.
//
// Inputs: cobra flags (--branch/--task/--session, mirroring
//
//	ScopeShowParams) plus a contextScopeDeps injected at construction so
//	no test touches the real environment or a real socket (Art.7.1).
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure. Read-only — never prompts, CASCADE_NO_INPUT has no
//	effect.
//
// Constraints: never a hand-rolled JSON-RPC request (hard requirement 1);
//
//	the embedded path never writes outside cascade.db.
//
// SPORT: cli/context-scope-show/ADD (per T-4 sport_updates).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// contextScopeDialTimeout bounds the daemon round trip, matching
// statusDialTimeout's established value.
const contextScopeDialTimeout = 5 * time.Second

// contextScopeDeps carries every external input this command needs,
// mirroring statusDeps's established injection pattern.
type contextScopeDeps struct {
	Paths       runtime.PathProvider
	Getenv      runtime.Getenv
	Environ     func() []string
	DialContext func(ctx context.Context, socketPath string) (net.Conn, error)
}

func productionContextScopeDeps() contextScopeDeps {
	return contextScopeDeps{
		Paths:       lazyPaths{},
		Getenv:      os.Getenv,
		Environ:     os.Environ,
		DialContext: client.UnixDialer,
	}
}

// mountContextCmd attaches the top-level `context` command group. Called
// from root.go's mountSubcommands; kept in this file (not root.go) so
// root.go's own addition stays a single call line under its 300-line cap.
func mountContextCmd(root *cobra.Command) {
	cmd := newContextCmd(productionContextScopeDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

func newContextCmd(deps contextScopeDeps) *cobra.Command {
	contextCmd := &cobra.Command{Use: "context", Short: "Session-scope and context-engine commands"}
	scopeCmd := &cobra.Command{Use: "scope", Short: "Session scope resolution and graph"}
	scopeCmd.AddCommand(newContextScopeShowCmd(deps))
	contextCmd.AddCommand(scopeCmd)
	return contextCmd
}

// newContextScopeShowCmd builds `context scope show`.
func newContextScopeShowCmd(deps contextScopeDeps) *cobra.Command {
	var params scope.ScopeShowParams
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Resolve and print this session's scope",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := fetchContextScopeShow(cmd.Context(), deps, params)
			if err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(contextScopeHumanView{
				SessionScope: result.SessionScope,
				Candidates:   result.Candidates,
			})
		},
	}
	cmd.Flags().StringVar(&params.Branch, "branch", "", "attach this branch to the resolved scope")
	cmd.Flags().StringVar(&params.Task, "task", "", "attach this active task id to the resolved scope")
	cmd.Flags().StringVar(&params.Session, "session", "", "attach this session id to the resolved scope")
	return cmd
}

// contextScopeResult carries the resolved SessionScope plus its
// deny-by-default Candidates (scope.CandidateScopeRefs), computed only in
// the embedded path (the daemon path returns Candidates=nil -- the wire
// contract's context.scope.show result is the bare SessionScope; a future
// ticket may extend it once a real cross-scope consumer needs the
// candidate set over RPC too).
type contextScopeResult struct {
	scope.SessionScope
	Candidates []scope.ScopeRef
}

// fetchContextScopeShow routes through the D/S-07.T3 client when the
// daemonless probe confirms a live daemon, and through the embedded
// runtime otherwise. An undecidable probe result (DaemonlessStateFrom's
// ok=false) is treated as embedded, matching root.go's own "unknown
// defaults to embedded" rule (probeDaemonlessAndAttach's doc comment).
func fetchContextScopeShow(ctx context.Context, deps contextScopeDeps, params scope.ScopeShowParams) (contextScopeResult, error) {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if ok && !st.Embedded {
		settings, err := daemon.ResolveSettings(nil, deps.Paths)
		if err != nil {
			return contextScopeResult{}, err
		}
		c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextScopeDialTimeout)
		result, err := c.ContextScopeShow(ctx, params)
		if err != nil {
			return contextScopeResult{}, err
		}
		return contextScopeResult{SessionScope: result}, nil
	}
	return resolveContextScopeEmbedded(ctx, deps, params)
}

// cliGitRoot is the production scope.GitRootFunc: `git rev-parse
// --show-toplevel`, cwd on any failure (git absent, not a repository,
// subprocess error). Lives here, not in internal/context/scope, because
// the A-T2 egress process-spawn allowlist (internal/build/egress_allow.go)
// already admits "cmd/cascade" as an "os/exec" importer -- see
// resolver.go's GitRootFunc doc comment for the full contract/tree note.
func cliGitRoot(ctx context.Context, cwd string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return cwd
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return cwd
	}
	return filepath.Clean(filepath.FromSlash(root))
}

// resolveContextScopeEmbedded is the D/S-07.T4 embedded runtime path: open
// cascade.db directly (matching daemon_unix_store.go's dbPath convention),
// apply the scope schema idempotently, and resolve in-process. This is
// the ONLY production caller of scope.ApplyScopeSchema, scope.NewGraphStore,
// and scope.CandidateScopeRefs — the composition-root wiring this ticket's
// mutation test proves reachable by removing mountContextCmd's call.
func resolveContextScopeEmbedded(ctx context.Context, deps contextScopeDeps, params scope.ScopeShowParams) (contextScopeResult, error) {
	if params.Cwd == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return contextScopeResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade context scope show: resolve cwd")
		}
		params.Cwd = cwd
	}
	dataDir := deps.Paths.DataDir()
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return contextScopeResult{}, cascade.Wrap(cascade.KindUnavailable, err, "cascade context scope show: open cascade.db")
	}
	defer func() { _ = db.Close() }()

	if err := scope.ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, runtime.NewSystemClock(), dbPath, filepath.Join(dataDir, "backups")); err != nil {
		return contextScopeResult{}, err
	}
	store := scope.NewGraphStore(db)
	deps2 := scope.ResolveDeps{Store: store, GitRoot: cliGitRoot}
	// Route through scope.ContextScopeShow (JSON in, SessionScope out)
	// rather than calling scope.ResolveSessionScope directly: it is the
	// one decode+resolve path this ticket shares with the daemon RPC
	// method and the MCP tool a later ticket wires on (see rpc.go's doc
	// comment and the journal's contract/tree note) -- routing the
	// embedded CLI path around it here would leave it built, tested, and
	// unreachable from this ticket's own shipping caller.
	raw, err := json.Marshal(params)
	if err != nil {
		return contextScopeResult{}, cascade.Wrap(cascade.KindInternal, err, "cascade context scope show: encode params")
	}
	resolved, err := scope.ContextScopeShow(ctx, deps2, raw)
	if err != nil {
		return contextScopeResult{}, err
	}
	candidates, err := scope.CandidateScopeRefs(ctx, store, scope.ScopeChain(resolved))
	if err != nil {
		return contextScopeResult{}, err
	}
	return contextScopeResult{SessionScope: resolved, Candidates: candidates}, nil
}

// contextScopeHumanView wraps scope.SessionScope (plus its computed
// Candidates) so this package can attach a human table String() method,
// mirroring statusHumanView.
type contextScopeHumanView struct {
	scope.SessionScope
	Candidates []scope.ScopeRef `json:"candidate_scopes,omitempty"`
}

// String renders the resolved scope as a human-readable table. A general
// (unresolved-cwd) result shows kind=general and every project-adjacent
// field blank, matching R-16.3's restricted-result contract.
func (v contextScopeHumanView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "FIELD\tVALUE\n")
	_, _ = fmt.Fprintf(tw, "kind\t%s\n", v.Kind)
	_, _ = fmt.Fprintf(tw, "user\t%s\n", v.User)
	_, _ = fmt.Fprintf(tw, "machine\t%s\n", v.Machine)
	_, _ = fmt.Fprintf(tw, "cwd\t%s\n", v.Cwd)
	_, _ = fmt.Fprintf(tw, "workspace\t%s\n", v.Workspace)
	_, _ = fmt.Fprintf(tw, "product\t%s\n", v.Product)
	_, _ = fmt.Fprintf(tw, "project\t%s\n", v.Project)
	if v.Repository != nil {
		_, _ = fmt.Fprintf(tw, "repository\t%s\n", v.Repository.Remote)
	}
	_, _ = fmt.Fprintf(tw, "package_path\t%s\n", v.PackagePath)
	_, _ = fmt.Fprintf(tw, "branch\t%s\n", v.Branch)
	_, _ = fmt.Fprintf(tw, "task\t%s\n", v.Task)
	_, _ = fmt.Fprintf(tw, "session\t%s\n", v.Session)
	_, _ = fmt.Fprintf(tw, "explicit_overrides\t%s\n", v.ExplicitOverrides)
	for _, c := range v.Candidates {
		_, _ = fmt.Fprintf(tw, "candidate_scope\t%s:%s\n", c.Kind, c.ID)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// contextScopeOutputWriter mirrors statusOutputWriter's established
// per-file convention.
func contextScopeOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}
