// Purpose: the `ci` command TREE (07-CLI-COMMAND-TREE §ci, R-16.21/R-16.31
// -- CORE noun, mounted directly on the cobra root, NOT a github-plugin
// verb) and the seams both its verbs share: CmdDeps, NewCICmd,
// outputWriter, openResultsDB, openRunDeps. This file lives in internal/ci
// rather than cmd/cascade/ci.go because this ticket's own files_scope
// names it here explicitly; cmd/cascade/root.go's "wire the import" task
// mounts it, matching cmd/cascade/config's identical separate-package
// precedent (NewConfigCmd lives in cmd/cascade/config, mounted by a thin
// root.go call) for the SAME reason R-21.273 exists: keeping the actual
// command logic out of cmd/cascade/main.go.
//
// The two verbs and the view types live beside this file -- `ci run` in
// run_cmd.go, `ci status` in status_cmd.go, the run's Result view in
// runner_view.go -- all split out purely to keep every file under
// Art.10.3's 300-line cap (R-14.117), matching domain_source.go's
// identical reason within this same ticket.
//
// Inputs: a CmdDeps injected at construction so no test touches the real
// environment, a real socket, or a real cascade.db.
// Outputs: a *cobra.Command tree, and the opened stores its verbs write
// through.
// Constraints: every `ci` verb is fully non-interactive (06 §5.8, no
// prompts) and daemonless (06 §2, embedded mode always -- no daemon RPC
// path exists for this domain). No new outbound network class: it only
// ever touches the local cascade.db and local sub-processes.
// SPORT: internal.ci.NewCICmd/ADDED (P1-E25-W5-S51-T5).

package ci

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CmdDeps carries every external input the `ci` command tree needs,
// mirroring cmd/cascade/config.Deps's established injection pattern.
type CmdDeps struct {
	Paths  runtime.PathProvider
	Getenv runtime.Getenv
	// Environ matches os.Environ's signature.
	Environ func() []string
	Clock   runtime.Clock
	// Routes is the never-pay policy resolver (route.go). Production gets
	// it from the composition bridge in internal/plugins via
	// InstalledRouteResolver; a test passes its own. A nil Routes makes
	// every routed verb fail closed rather than run unguarded.
	Routes RouteResolver
}

// ProductionCmdDeps builds CmdDeps against the real environment --
// runner_cmd.go's only caller of runtime.NewDefaultPathProvider, matching
// lazyPaths's own deferred-resolution reasoning (a resolution failure
// surfaces when the command actually runs, not at tree-construction time,
// so `cascade --help` never depends on the environment).
func ProductionCmdDeps() CmdDeps {
	return CmdDeps{
		Getenv:  os.Getenv,
		Environ: os.Environ,
		Clock:   runtime.NewSystemClock(),
		Routes:  InstalledRouteResolver(),
	}
}

// resolvePaths resolves deps.Paths on first use if the caller left it
// nil (the production zero-value path), so ProductionCmdDeps can stay a
// cheap, environment-touching-free constructor.
func (d CmdDeps) resolvePaths() (runtime.PathProvider, error) {
	if d.Paths != nil {
		return d.Paths, nil
	}
	return runtime.NewDefaultPathProvider()
}

// NewCICmd builds the `ci` command tree.
func NewCICmd(deps CmdDeps) *cobra.Command {
	root := &cobra.Command{
		Use:   "ci",
		Short: "Run the local CI gate, or inspect recorded CI results",
	}
	root.AddCommand(newCIRunCmd(deps))
	root.AddCommand(newCIStatusCmd(deps))
	return root
}

// outputWriter builds an internal/output.Writer bound to cmd's own
// streams and the standard global flags, tolerating any being
// unregistered -- matching cmd/cascade/config's identical helper.
func outputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// openResultsDB opens (creating dataDir if absent, matching
// resolveContextSliceEmbedded's identical gap-fix) and schema-applies the
// ci_results domain's raw *sql.DB -- the SAME connection style every
// other embedded one-shot command in cmd/cascade uses for its own
// migrate-DSL-backed tables (context_cmd.go's resolveContextSliceEmbedded
// is this file's direct precedent).
func openResultsDB(ctx context.Context, dataDir string, clock runtime.Clock) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: create data directory")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: open cascade.db")
	}
	// runtime.Clock and migrate.Clock are two independently-declared but
	// structurally identical interfaces (Now() time.Time -- domain.go's
	// own Clock doc comment makes the same point for THIS package's local
	// Clock type); Go satisfies one interface parameter from another
	// interface-typed argument automatically when their method sets
	// match, so clock passes through with no adapter.
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(dataDir, "backups")); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// journalStoreDuckType documents (does not enforce beyond a compile
// check) that *journal.SQLiteStore satisfies this package's own
// JournalStore interface (runner.go) with no adapter -- exactly the same
// duck-typing internal/conversation/journal.go's own JournalStore
// interface relies on for the identical type.
var _ JournalStore = (*journal.SQLiteStore)(nil)

// openRunDeps opens every store `ci run` writes through: the raw
// ci_results *sql.DB and the §D-3 write-arbitrated embedded provider.Store
// backing Events/Journal. The returned closer always closes both,
// regardless of which (if either) opened successfully far enough to need
// it.
func openRunDeps(ctx context.Context, dataDir string, clock runtime.Clock) (Deps, func(), error) {
	db, err := openResultsDB(ctx, dataDir, clock)
	if err != nil {
		return Deps{}, func() {}, err
	}
	store, err := runtime.OpenEmbeddedWriteStore(ctx, filepath.Join(dataDir, "cascade.db"), nil, nil)
	if err != nil {
		_ = db.Close()
		return Deps{}, func() {}, err
	}
	closer := func() {
		_ = store.Close()
		_ = db.Close()
	}
	return Deps{
		DB:      db,
		Events:  events.New(store, clock),
		Journal: journal.New(store, clock, journal.DefaultNamespace),
		Clock:   clock,
		Exec:    ShellExecutor{},
	}, closer, nil
}
