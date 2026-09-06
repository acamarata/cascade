package daemon

// Purpose: registers context.slice and context.show (E/S-09.T2) on the
//   daemon's RPC router, and holds the glue both this handler and
//   cmd/cascade's embedded fallback (cmd/cascade/context_cmd.go) call: run
//   the S-08 discover+merge pipeline over a cwd, and — for context.slice
//   only — resolve the caller's session scope and call the S-09.T1
//   assembler (internal/context.Assemble). Split out of context_scope.go
//   (which registers the sibling context.scope.show method) for the same
//   300-line-cap reason that file's own doc comment names.
//
// Inputs: the daemon's shared *rpc.Registry, its runtime.PathProvider, and
//   its runtime.Clock — the same three buildRPCServer threads into every
//   sibling registerXHandler (daemon_unix_run.go).
//
// Outputs: context.slice and context.show, bound to a real modernc SQLite
//   connection under paths.DataDir()/cascade.db (context.slice's scope
//   resolution needs it; context.show needs no store at all) and a real
//   `git rev-parse --show-toplevel` GitRootFunc (gitRootExec, defined in
//   context_scope.go, this package).
//
// Constraints: opens its OWN *sql.DB handle, for the exact reason
//   context_scope.go's RegisterContextScopeHandler documents (threading
//   platformDaemonRun's rawDB into buildRPCServer's signature would ripple
//   into call sites outside this ticket's files_scope); a second
//   connection to the same file is a documented no-op after the first
//   opens it, since ApplyScopeSchema is idempotent by contract. No
//   retrieval or memory source is wired into context.slice yet: the RRF
//   fusion output (F/S-11) and a real provider.MemoryReader are both
//   separate tickets' work, so Assemble is called with Ranked/Reader nil
//   — a documented, legal input (see internal/context/assembly.go's own
//   doc comment: nil Ranked yields an empty retrieval slot, nil Reader an
//   empty memory slot, never an error). The token counter is
//   provider.NaiveTokenCounter, the ONE TokenCounter implementation that
//   ships anywhere in the tree today; no real tokenizer exists yet either.
//   No profile-level BudgetConfig section exists in internal/runtime.Config
//   today (grep of that package turns up none), so this file resolves the
//   budget from a local, documented default (defaultContextMaxTokens)
//   rather than "the active profile's BudgetConfig" the contract names —
//   see this ticket's journal for the quoted contradiction.
//
// SPORT: internal/daemon (ADD, context.slice + context.show registration,
//   E/S-09.T2).

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ContextSliceMethod and ContextShowMethod are the context.slice and
// context.show JSON-RPC method names (E/S-09.T2), matching
// ContextScopeMethod's established cross-reference pattern (daemon.go):
// internal/client's callers dial these exact literals via Client.Do.
const (
	ContextSliceMethod = "context.slice"
	ContextShowMethod  = "context.show"
)

// defaultContextMaxTokens is the token budget context.slice uses when the
// caller supplies no override. See this file's top doc comment: no real
// per-profile budget configuration exists in the tree yet.
const defaultContextMaxTokens = 8000

// ContextAssembleParams is the shared wire params for context.slice and
// context.show. Cwd is always caller-supplied: the daemon process's own
// working directory is meaningless here, so both cmd/cascade call sites
// (the client dial and the embedded fallback) resolve their own cwd before
// sending it. MaxTokens is a pointer so an explicit 0 (an invalid budget,
// asserted by BudgetConfig.Validate) is distinguishable from "unset" —a
// plain int would make the two indistinguishable and silently fall back to
// the default instead of surfacing the caller's typed error.
type ContextAssembleParams struct {
	Cwd       string `json:"cwd"`
	MaxTokens *int   `json:"max_tokens,omitempty"`
}

// ContextTierView is one rendered tier in a context.show result.
type ContextTierView struct {
	Name    string `json:"name"`
	Ordinal int    `json:"ordinal"`
	Content string `json:"content"`
	Tokens  int    `json:"tokens"`
}

// ContextShowResult is context.show's wire result.
type ContextShowResult struct {
	Tiers []ContextTierView `json:"tiers"`
}

// ContextSliceResult is context.slice's wire result: the assembled
// context's slots plus its per-slot token accounting, flattened from
// provider.ContextAssembly.
type ContextSliceResult struct {
	Tier      []provider.TierBlock      `json:"tier"`
	Retrieval []provider.RetrievedChunk `json:"retrieval"`
	Memory    []provider.MemoryItem     `json:"memory"`
	Counts    provider.SlotCounts       `json:"counts"`
	Dropped   []provider.DroppedItem    `json:"dropped"`
}

// RegisterContextAssembleHandler opens the scope graph's SQLite connection
// (the same documented second-connection tradeoff RegisterContextScopeHandler
// makes; see this file's top doc comment) and registers context.slice and
// context.show against registry. Returns the opened *sql.DB so the caller
// can close it during daemon shutdown; a non-nil error means no db was left
// open.
func RegisterContextAssembleHandler(registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) (*sql.DB, error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: context.slice/show: create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: context.slice/show: open cascade.db")
	}
	backupDir := filepath.Join(paths.DataDir(), "backups")
	if err := scope.ApplyScopeSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, dbPath, backupDir); err != nil {
		_ = db.Close()
		return nil, err
	}

	deps := scope.ResolveDeps{Store: scope.NewGraphStore(db), GitRoot: gitRootExec}
	registry.Register(ContextShowMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeContextAssembleParams(raw)
		if err != nil {
			return nil, err
		}
		return ComputeContextShow(ctx, p.Cwd)
	})
	registry.Register(ContextSliceMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeContextAssembleParams(raw)
		if err != nil {
			return nil, err
		}
		return ComputeContextSlice(ctx, deps, p)
	})
	return db, nil
}

// decodeContextAssembleParams decodes raw into ContextAssembleParams,
// failing closed on malformed input rather than proceeding with a
// zero-value guess.
func decodeContextAssembleParams(raw json.RawMessage) (ContextAssembleParams, error) {
	var p ContextAssembleParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ContextAssembleParams{}, cascade.Wrap(cascade.KindInvalidInput, err, "daemon: context: malformed params")
		}
	}
	return p, nil
}

// ComputeContextShow runs the S-08 discover+merge pipeline over cwd and
// renders every surviving section as a tier view, token counts included
// (provider.NaiveTokenCounter — never errors). Shared by the RPC handler
// above and cmd/cascade's embedded fallback: neither needs a store for
// this verb, so it takes no ResolveDeps.
func ComputeContextShow(ctx context.Context, cwd string) (ContextShowResult, error) {
	merged, err := discoverMerged(ctx, cwd)
	if err != nil {
		return ContextShowResult{}, err
	}
	views := make([]ContextTierView, 0, len(merged.Sections))
	counter := provider.NaiveTokenCounter{}
	for i, sec := range merged.Sections {
		name := sec.Role.String()
		if sec.Heading != "" {
			name += ": " + sec.Heading
		}
		n, _ := counter.Count(ctx, sec.Content) // NaiveTokenCounter never errors
		views = append(views, ContextTierView{Name: name, Ordinal: i, Content: sec.Content, Tokens: n})
	}
	return ContextShowResult{Tiers: views}, nil
}

// ComputeContextSlice runs the same discover+merge pipeline, resolves the
// caller's session scope over deps, and calls internal/context.Assemble.
// Shared by the RPC handler above (its own DB-backed deps) and
// cmd/cascade's embedded fallback (its own, separately-opened DB-backed
// deps — see context_cmd.go), exactly as ContextScopeShow is shared by
// both context_scope.go composition roots.
func ComputeContextSlice(ctx context.Context, deps scope.ResolveDeps, p ContextAssembleParams) (ContextSliceResult, error) {
	merged, err := discoverMerged(ctx, p.Cwd)
	if err != nil {
		return ContextSliceResult{}, err
	}
	sc, err := scope.ResolveSessionScope(ctx, deps, scope.ResolveInput{Cwd: p.Cwd})
	if err != nil {
		return ContextSliceResult{}, err
	}
	maxTokens := defaultContextMaxTokens
	if p.MaxTokens != nil {
		maxTokens = *p.MaxTokens
	}
	assembly, err := cascadecontext.Assemble(ctx, cascadecontext.AssembleInput{
		Scope:   sc,
		Merged:  &merged,
		Counter: provider.NaiveTokenCounter{},
		Budget:  provider.BudgetConfig{MaxTokens: maxTokens},
	})
	if err != nil {
		return ContextSliceResult{}, err
	}
	return ContextSliceResult{
		Tier: assembly.Tier, Retrieval: assembly.Retrieval, Memory: assembly.Memory,
		Counts: assembly.Counts, Dropped: assembly.Dropped,
	}, nil
}

// discoverMerged runs Discover then MergeTiers for cwd — the same
// two-call sequence internal/context's own harness pipeline
// (GenerateHarnessInstructions) uses — passing os.UserHomeDir directly, the
// production convention discover.go's HomeDirFunc doc comment documents.
func discoverMerged(ctx context.Context, cwd string) (cascadecontext.MergedContext, error) {
	records, err := cascadecontext.Discover(ctx, cwd, os.UserHomeDir)
	if err != nil {
		return cascadecontext.MergedContext{}, err
	}
	return cascadecontext.MergeTiers(records)
}
