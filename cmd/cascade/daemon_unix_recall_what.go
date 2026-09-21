//go:build !windows

// Purpose: D1 -- the recall.what composition root (P1-E22-W5-S47-T1,
// standing T0 ruling: "composition roots are in scope", precedents
// S-48.T1/S-48.T3/S-51.T4/S-46.T4/S-50.T4). Builds the real
// *retrieval.RecallWhatHandler: the files leg (a fresh recall.Service,
// mirroring registerRecallHandler's own construction), the conversation
// leg (a real conversation.Store over its own sqlite connection,
// mirroring wireChatHandlers' pattern), the memory leg (a real
// *memory.ProjectionJob over the SAME cascade.db store buildRPCServer
// already opened, D2), the scope resolver (context/scope.ResolveSessionScope
// over its own sqlite connection, mirroring RegisterContextScopeHandler),
// and the egress boundary (daemonEgressFirewall, the same firewall
// constructor the conductor security pipeline uses).
//
// THIS FILE DOES NOT IMPORT internal/rpc. It builds the service and
// returns a *retrieval.RecallWhatHandler; the actual registry.Register
// call is the one-line hook in daemon_unix_memory_recall.go, a
// cmd-rpc-server-boundary-exempt file (build-lane-rules item 17/19: "do
// the rpc.Registry call from an exempt file"). daemon_unix_memory_recall.go
// is outside this ticket's ORIGINAL files_scope; this is a recorded scope
// deviation, the same shape build-lane-rules item 19 itself anticipates
// ("add a NEW sibling wiring file... never skip the wiring").
//
// Inputs: the daemon's shared runtime.PathProvider, runtime.Clock,
// *events.Bus and provider.Store (buildRPCServer already threads all four
// into registerMemoryAndRecall).
// Outputs: a *retrieval.RecallWhatHandler, or a construction error. A nil
// handler with a nil error never happens: recall.what is either wired or
// the daemon fails to start, matching every sibling registerXHandler's
// own contract.
//
// Constraints: a THIRD sqlite connection to the same cascade.db file
// (conversation) and a FOURTH (scope) -- both documented no-ops after the
// first opens each schema (registerContextEngineHandlers,
// wireConductorExpand and wireChatHandlers already establish this exact
// tradeoff three times over; this is the same choice a fourth and fifth
// time, not a new one).
//
// SPORT: cmd/cascade/daemon (ADD, P1-E22-W5-S47-T1, D1).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// buildRecallWhatHandler builds D1's real *retrieval.RecallWhatHandler.
func buildRecallWhatHandler(
	paths runtime.PathProvider, clock runtime.Clock, bus *events.Bus, store provider.Store,
) (*retrieval.RecallWhatHandler, error) {
	files, err := recallWhatFilesLeg(paths, bus, store)
	if err != nil {
		return nil, err
	}
	conv, err := recallWhatConversationLeg(clock, paths)
	if err != nil {
		return nil, err
	}
	var mem retrieval.RecallWhatMemoryLeg
	if store != nil {
		mem = memory.NewProjectionJob(memory.NewFileStore(memoryStoreDir(paths), clock), store, nil, nil, clock)
	}
	resolver, err := recallWhatScopeResolver(paths)
	if err != nil {
		return nil, err
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, err
	}
	firewall, err := daemonEgressFirewall(paths, detector)
	if err != nil {
		return nil, err
	}
	svc, err := retrieval.NewRecallWhatService(files, conv, mem, rrf.Params{}, clock, resolver, firewall)
	if err != nil {
		return nil, err
	}
	return retrieval.NewRecallWhatHandler(svc), nil
}

// recallWhatFilesLeg mirrors registerRecallHandler's own construction
// (daemon_unix_handlers.go): the SAME catalog path and full-text leg over
// the SAME store, so recall.what's files answers agree with `cascade
// recall`'s. A separate *recall.Service instance is unavoidable here:
// registerRecallHandler builds and discards its own rather than returning
// one, and widening its signature is out of this ticket's files_scope.
func recallWhatFilesLeg(paths runtime.PathProvider, bus *events.Bus, store provider.Store) (retrieval.RecallWhatFilesLeg, error) {
	catalog := recall.NewFileCatalog(filepath.Join(recallIndexDir(paths), recall.CatalogFileName))
	legs := []recall.Leg{fusion.NewVectorLeg(nil, nil, bus)}
	if store != nil {
		idx, err := retrieval.NewIndex(store)
		if err != nil {
			return nil, err
		}
		legs = append(legs, retrieval.NewLeg(idx))
	}
	return recall.NewService(catalog, rrf.Params{}, legs...)
}

// recallWhatConversationLeg opens its own sqlite connection to
// cascade.db, applies the conversation schema (idempotent after
// wireChatHandlers' own apply), and returns a real conversation.Store
// wrapped in conversationLegAdapter -- internal/retrieval's
// RecallWhatConversationLeg speaks retrieval-owned types (D1cycle fix:
// retrieval must never import internal/conversation, see
// internal/retrieval/recallwhat_conv.go's header), so this composition
// root, which is free to import both packages, is where the two type
// systems meet.
func recallWhatConversationLeg(clock runtime.Clock, paths runtime.PathProvider) (retrieval.RecallWhatConversationLeg, error) {
	db, err := openSecondaryCascadeDB(paths, "recall.what: conversation leg")
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	backupDir := filepath.Join(paths.DataDir(), "backups")
	if err := conversation.ApplyConversationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, dbPath, backupDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	return conversationLegAdapter{store: conversation.NewStore(db)}, nil
}

// conversationLegSource is the minimal conversation.Store surface
// conversationLegAdapter reads -- narrower than the full Store interface
// so a test can satisfy it with a fake carrying only these three
// methods; conversation.Store satisfies it with no changes.
type conversationLegSource interface {
	SearchTurns(ctx context.Context, query string, filter conversation.SearchFilter) ([]conversation.TurnMatch, error)
	ListSegments(ctx context.Context, turnID string) ([]conversation.Segment, error)
	ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error)
}

// conversationLegAdapter adapts a conversationLegSource (a real
// conversation.Store in production) to retrieval.RecallWhatConversationLeg's
// retrieval-owned types -- the D1cycle fix's composition-root half.
type conversationLegAdapter struct{ store conversationLegSource }

// SearchTurns implements retrieval.RecallWhatConversationLeg.
func (a conversationLegAdapter) SearchTurns(ctx context.Context, query string, filter retrieval.RecallWhatSearchFilter) ([]retrieval.RecallWhatTurnMatch, error) {
	matches, err := a.store.SearchTurns(ctx, query, conversation.SearchFilter{ThreadID: filter.ThreadID, Limit: filter.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]retrieval.RecallWhatTurnMatch, len(matches))
	for i, m := range matches {
		out[i] = retrieval.RecallWhatTurnMatch{
			Turn: retrieval.RecallWhatTurn{ID: m.Turn.ID, ThreadID: m.Turn.ThreadID, Role: string(m.Turn.Role)},
			Rank: m.Rank,
		}
	}
	return out, nil
}

// ListSegments implements retrieval.RecallWhatConversationLeg.
func (a conversationLegAdapter) ListSegments(ctx context.Context, turnID string) ([]retrieval.RecallWhatSegment, error) {
	segs, err := a.store.ListSegments(ctx, turnID)
	if err != nil {
		return nil, err
	}
	out := make([]retrieval.RecallWhatSegment, len(segs))
	for i, s := range segs {
		out[i] = retrieval.RecallWhatSegment{Content: s.Content}
	}
	return out, nil
}

// ThreadPrivacy implements retrieval.RecallWhatConversationLeg.
func (a conversationLegAdapter) ThreadPrivacy(ctx context.Context, threadID string) (provider.SensitivityTier, error) {
	return a.store.ThreadPrivacy(ctx, threadID)
}

// recallWhatScopeResolver opens its own sqlite connection to cascade.db,
// applies the scope schema (idempotent after RegisterContextScopeHandler's
// own apply), and returns a retrieval.ScopeResolver bound to
// scope.ResolveSessionScope -- the SAME resolver context.scope.show uses
// (internal/daemon/context_scope.go), so a resolved scope means the same
// thing on both surfaces.
func recallWhatScopeResolver(paths runtime.PathProvider) (retrieval.ScopeResolver, error) {
	db, err := openSecondaryCascadeDB(paths, "recall.what: scope resolver")
	if err != nil {
		return nil, err
	}
	backupDir := filepath.Join(paths.DataDir(), "backups")
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	if err := scope.ApplyScopeSchema(context.Background(), db, migrate.SQLiteEmitter{}, runtime.NewSystemClock(), dbPath, backupDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	return recallWhatResolver{deps: scope.ResolveDeps{Store: scope.NewGraphStore(db), GitRoot: cliGitRoot}}, nil
}

// recallWhatResolver adapts scope.ResolveSessionScope to
// retrieval.ScopeResolver, the identical adapter shape
// internal/conductor/sensitivity.go's EgressSubstitutor documents for the
// egress seam.
type recallWhatResolver struct{ deps scope.ResolveDeps }

func (r recallWhatResolver) Resolve(ctx context.Context, in scope.ResolveInput) (scope.SessionScope, error) {
	return scope.ResolveSessionScope(ctx, r.deps, in)
}

// openSecondaryCascadeDB opens a fresh connection to
// paths.DataDir()/cascade.db, the same busy_timeout the daemon's other
// secondary connections use (registerContextEngineHandlers,
// wireConductorExpand, wireChatHandlers).
func openSecondaryCascadeDB(paths runtime.PathProvider, what string) (*sql.DB, error) {
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: "+what+": create data dir")
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "daemon: "+what+": open cascade.db")
	}
	return db, nil
}
