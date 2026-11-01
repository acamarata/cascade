package daemon

// Purpose: registers the recall.index.rebuild|verify|migrate|update
// namespace (F/S-11.T4) on the daemon's RPC router, over a real
// internal/retrieval/lifecycle.Manager, and supplies the real git tree
// generation marker (R-21.189) and git-diff functions that
// package deliberately never imports os/exec to compute itself.
//
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider and
// runtime.Clock (buildRPCServer already threads all three into every
// sibling registerXHandler), and the already-open provider.Store
// (cmd/cascade/daemon_unix_store.go's openRuntimeStore) the FTS5 leg and
// this package's own manifest/marker bookkeeping read and write.
//
// Outputs: internal/retrieval/lifecycle's four RPC methods, bound to a
// real Manager over the real store; migrate additionally opens its own
// second sqlite connection to apply the retrieval domain's MigrationSet,
// mirroring context_scope.go's documented "second connection" choice. The
// Manager's Sources is configSourceProvider (recall_index_sources.go),
// reading retrieval.sources[] out of config.toml fresh on every call —
// closing DEFECT-retrieval-sources-not-wired-to-ingest.md, the gap that
// left `cascade recall index rebuild` unable to index any real project.
//
// Constraints: os/exec lives ONLY in this file's two GitTreeHashFunc/
// GitDiffFunc implementations — internal/daemon is already an
// EgressExecNotYetMigrated os/exec importer (context_scope.go's
// gitRootExec doc comment), so this adds no new unlisted importer to the
// A-T2 arch gate. A nil store (a test's minimal buildRPCServer call)
// disables this namespace entirely rather than registering a handler
// that would panic on first use — the same degradation
// registerMemoryHandler already applies to a nil memoryAdmin.
//
// SPORT: internal/daemon (CHANGED — recall.index.* registration,
// P1-E06-W2-S11-T4; CHANGED again, retrieval.sources ingest wiring;
// CHANGED P1-CPA-11, RecallEmbedding embeds admitted corpora only).
import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RecallIndexRebuildMethod, RecallIndexVerifyMethod, RecallIndexMigrateMethod
// and RecallIndexUpdateMethod are the recall.index.* JSON-RPC method names
// (07-CLI-COMMAND-TREE §recall).
const (
	RecallIndexRebuildMethod = "recall.index.rebuild"
	RecallIndexVerifyMethod  = "recall.index.verify"
	RecallIndexMigrateMethod = "recall.index.migrate"
	RecallIndexUpdateMethod  = "recall.index.update"
)

// RecallEmbedding is the embedding half recall.index.* writes with: the
// process's one Selection's embedder, the vector store the query-time leg
// reads, and the admission rule over corpora (the Selection's tier rule
// over each corpus's privacy class). A zero value is no embedder.
type RecallEmbedding struct {
	// Embedder embeds admitted corpora. Nil means no vector leg is written.
	Embedder provider.Embedder
	// Vectors is the vector store. Nil means no vector leg is written.
	Vectors provider.VectorStore
	// AdmitCorpus reports whether a corpus's content may be embedded. Nil
	// with an Embedder admits nothing.
	AdmitCorpus func(corpus.Corpus) bool
}

// RegisterRecallIndexHandler mounts the recall.index.* namespace on
// registry, over a Manager built against store. A nil store leaves the
// namespace unregistered: a build with no runtime store configured (a
// minimal test harness) is not this ticket's problem to solve, and an
// unknown-method response is the correct, honest answer for it — the
// same choice registerMemoryHandler's nil-admin branch already makes.
//
// emb is at most one RecallEmbedding. With an Embedder and Vectors the
// Manager embeds every corpus emb.AdmitCorpus admits and Verify checks only
// those; otherwise no vector leg is written or counted
// (DEFECT-vector-leg-never-written-on-rebuild.md: a vector store nothing
// writes made `cascade doctor` warn vector-incomplete on every install).
func RegisterRecallIndexHandler(
	registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock, store provider.Store, dbPath string,
	emb ...RecallEmbedding,
) error {
	if store == nil {
		return nil
	}
	if len(emb) > 1 {
		return cascade.New(cascade.KindInvalidInput, "recall index: at most one RecallEmbedding")
	}
	opts := lifecycle.ManagerOptions{
		CatalogPath: filepath.Join(recallIndexDataDir(paths), recallIndexCatalogName),
		Store:       store,
		Index:       mustIndex(store),
		Sources:     configSourceProvider{configPath: paths.ConfigPath()},
		Clock:       clock,
		TreeHash:    GitTreeHash,
	}
	if len(emb) == 1 {
		if err := withRecallEmbedding(&opts, emb[0], store, clock); err != nil {
			return err
		}
	}
	manager, err := lifecycle.NewManager(opts)
	if err != nil {
		return err
	}
	registry.Register(RecallIndexRebuildMethod, recallIndexRebuildHandler(manager))
	registry.Register(RecallIndexVerifyMethod, recallIndexVerifyHandler(manager))
	registry.Register(RecallIndexUpdateMethod, recallIndexUpdateHandler(manager))
	registry.Register(RecallIndexMigrateMethod, recallIndexMigrateHandler(clock, dbPath))
	return nil
}

// withRecallEmbedding sets opts' vector leg from emb: the pipeline over
// emb's embedder and vectors, keeping its bookkeeping in store, and the
// admission rule (nil admits nothing). An incomplete emb sets nothing.
func withRecallEmbedding(opts *lifecycle.ManagerOptions, emb RecallEmbedding, store provider.Store, clock runtime.Clock) error {
	if emb.Embedder == nil || emb.Vectors == nil {
		return nil
	}
	pipeline, err := embed.New(emb.Embedder, emb.Vectors, store, clock, 0)
	if err != nil {
		return err
	}
	admit := emb.AdmitCorpus
	if admit == nil {
		admit = func(corpus.Corpus) bool { return false }
	}
	opts.Vectors, opts.Pipeline, opts.EmbedAdmits = emb.Vectors, pipeline, admit
	return nil
}

// recallIndexCatalogName is the catalog document's file name within
// recallIndexDataDir(paths), matching recall.CatalogFileName
// (cmd/cascade/daemon_unix_handlers.go's recallIndexDir builds the same
// {DataDir}/retrieval directory; that helper is unexported to package
// main, so this file computes the same path independently rather than
// reaching across the cmd/internal boundary).
const recallIndexCatalogName = "catalog.json"

// recallIndexDataDir is {CASCADE_HOME}/data/retrieval, matching
// cmd/cascade/daemon_unix_handlers.go's recallIndexDir exactly.
func recallIndexDataDir(paths runtime.PathProvider) string {
	return filepath.Join(paths.DataDir(), "retrieval")
}

// mustIndex builds the FTS5 leg over store. retrieval.NewIndex only
// errors on a nil store, which this call site never passes.
func mustIndex(store provider.Store) *retrieval.Index {
	idx, _ := retrieval.NewIndex(store)
	return idx
}

// recallIndexRebuildHandler serves recall.index.rebuild.
func recallIndexRebuildHandler(m *lifecycle.Manager) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) { return m.Rebuild(ctx) }
}

// recallIndexVerifyHandler serves recall.index.verify.
func recallIndexVerifyHandler(m *lifecycle.Manager) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) { return m.Verify(ctx) }
}

// recallIndexUpdateHandler serves recall.index.update.
func recallIndexUpdateHandler(m *lifecycle.Manager) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) { return m.Update(ctx, gitDiffExec) }
}

// recallIndexMigrateHandler serves recall.index.migrate, opening its own
// second sqlite connection to dbPath for the duration of one call —
// context_scope.go's documented tradeoff, applied here for the same
// reason: threading a *sql.DB into buildRPCServer's signature would touch
// every existing call site and test, none of which are this ticket's.
func recallIndexMigrateHandler(clock runtime.Clock, dbPath string) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
		if err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "recall.index.migrate: open cascade.db")
		}
		defer func() { _ = db.Close() }()
		return lifecycle.Migrate(ctx, lifecycle.MigrateDeps{
			DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock,
			DBPath: dbPath, BackupDir: filepath.Join(filepath.Dir(dbPath), "backups"),
		})
	}
}

// gitDiffExec is the production lifecycle.GitDiffFunc: the paths that
// changed between sinceTreeHash's commit and the current working tree.
// sinceTreeHash carries GitTreeHash's own "<commit>:<digest>" shape;
// only the commit half is meaningful to `git diff`.
func gitDiffExec(ctx context.Context, sinceTreeHash string) ([]lifecycle.ChangedPath, string, error) {
	commit, _, _ := strings.Cut(sinceTreeHash, ":")
	newTree, err := GitTreeHash(ctx)
	if err != nil || commit == "" {
		return nil, newTree, err
	}
	out, err := recallIndexRunGit(ctx, "diff", "--name-status", commit, "--")
	if err != nil {
		return nil, newTree, nil //nolint:nilerr // a diff against an unknown commit is treated as "nothing to report"
	}
	return parseNameStatus(out), newTree, nil
}

// parseNameStatus turns `git diff --name-status` output into ChangedPaths.
func parseNameStatus(out string) []lifecycle.ChangedPath {
	var changed []lifecycle.ChangedPath
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		changed = append(changed, lifecycle.ChangedPath{Path: fields[len(fields)-1], Deleted: fields[0] == "D"})
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	return changed
}

// recallIndexRunGit runs `git <args>` in the current working directory, returning
// stdout with surrounding whitespace trimmed.
func recallIndexRunGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
