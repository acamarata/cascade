package conversation

// Purpose: caller-principal scoping for the chat.* verbs. A thread is
//   attributed once, at creation, to the creating caller's scope (a
//   principal's project scope, or "owner" for the owner CLI); every chat
//   read and append is filtered by the caller's view of those rows.
// Inputs: the transport-verified Caller on ctx; conversation_thread_scope.
// Outputs: CallerThreadView, scoped read handlers, the append guard. A principal
//   gets one KindPermissionDenied for a foreign, owner, unattributed or absent thread.
// Constraints: authority comes only from the Caller, never from params; a
//   declared scope narrows a view and is refused unless it is the
//   principal's own. Rows are create-only; an append never attributes an
//   existing thread, so pre-migration threads never reach a principal.
// SPORT: internal.conversation.adapter (scoped reads).

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	tableThreadScope = "conversation_thread_scope"
	// ownerScope attributes owner-CLI threads; no principal can hold it.
	ownerScope         = "owner"
	projectScopePrefix = "project:"
)

var (
	errScopeDenied   = cascade.New(cascade.KindPermissionDenied, "conversation: thread outside caller scope")
	errScopeConflict = cascade.New(cascade.KindConflict, "conversation: thread already attributed to another scope")
	errScopeStore    = cascade.New(cascade.KindUnavailable, "conversation: thread scope needs the SQL conversation store")
	errScopeParam    = cascade.New(cascade.KindInvalidInput, "conversation: scope must be a non-empty string")

	scopeSchemaMu    sync.Mutex
	scopeSchemaReady = map[*sql.DB]bool{}
)

// scopeMigrationSet is the versioned thread_scope schema (own SetID, R-16.77).
// No foreign key: the row is written before the creating turn commits.
func scopeMigrationSet() migrate.MigrationSet {
	return migrate.MigrationSet{
		SetID:         "conversation-thread-scope",
		SchemaVersion: 1,
		ReaderCeiling: 1,
		Steps: []migrate.MigrationStep{{
			Kind:        migrate.StepCreateTable,
			Description: "conversation_thread_scope: the creating caller's scope, written once",
			Table: &migrate.TableDef{
				Name:    tableThreadScope,
				Columns: []migrate.ColumnDef{id("thread_id"), text("scope_ref")},
			},
		}},
	}
}

// ensureScopeSchema applies scopeMigrationSet to db once per process.
func ensureScopeSchema(ctx context.Context, db *sql.DB, clock Clock) error {
	scopeSchemaMu.Lock()
	defer scopeSchemaMu.Unlock()
	if scopeSchemaReady[db] {
		return nil
	}
	if err := migrate.Apply(ctx, migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: clock}, scopeMigrationSet()); err != nil {
		return err
	}
	scopeSchemaReady[db] = true
	return nil
}

// scopeDB returns s's database with the thread_scope schema applied.
func scopeDB(ctx context.Context, s Store, clock Clock) (*sql.DB, error) {
	cs, ok := s.(*conversationStore)
	if !ok {
		return nil, errScopeStore
	}
	return cs.db, ensureScopeSchema(ctx, cs.db, clock)
}

// attributeThread records threadID's scope inside tx (a chat.create_thread
// handler joins it to its own creating transaction). Create-only: the same scope again is a no-op, another
// scope is KindConflict and the stored row is unchanged.
func attributeThread(tx *sql.Tx, threadID, scopeRef string) error {
	if threadID == "" || scopeRef == "" {
		return ErrInvalidRecord
	}
	res, err := tx.Exec(`INSERT INTO `+tableThreadScope+` (thread_id, scope_ref) VALUES (?, ?) ON CONFLICT(thread_id) DO NOTHING`,
		threadID, scopeRef)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: attribute thread")
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return nil
	}
	var stored string
	if err := tx.QueryRow(`SELECT scope_ref FROM `+tableThreadScope+` WHERE thread_id = ?`, threadID).Scan(&stored); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: read thread attribution")
	}
	if stored != scopeRef {
		return errScopeConflict
	}
	return nil
}

// scopedThreadIDs returns every thread attributed to scope.
func scopedThreadIDs(ctx context.Context, db *sql.DB, scope string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT thread_id FROM `+tableThreadScope+` WHERE scope_ref = ?`, scope)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: read thread scope")
	}
	defer func() { _ = rows.Close() }()
	ids := map[string]bool{}
	var id string
	for err == nil && rows.Next() {
		if err = rows.Scan(&id); err == nil {
			ids[id] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "conversation: read thread scope")
	}
	return ids, nil
}

// callerPrincipal returns the principal on ctx; ok is false for the owner CLI
// and in-process callers (the registry refuses a missing Caller first).
func callerPrincipal(ctx context.Context) (principal.Principal, bool) {
	c, ok := principal.CallerFrom(ctx)
	if !ok || c.Principal == nil {
		return principal.Principal{}, false
	}
	return *c.Principal, true
}

// projectScope reports whether scope is a usable project scope.
func projectScope(scope string) bool {
	return strings.HasPrefix(scope, projectScopePrefix) && len(scope) > len(projectScopePrefix)
}

// threadView is the set of threads one caller may see; all is the owner view.
type threadView struct {
	all bool
	ids map[string]bool
}

func (v threadView) admits(threadID string) bool { return v.all || v.ids[threadID] }

// resolveView builds the caller's view: a principal sees its own scope and is
// refused any other declared one; the owner sees all, or the declared scope.
func resolveView(ctx context.Context, s Store, clock Clock, declared string) (threadView, error) {
	scope := declared
	if p, isPrincipal := callerPrincipal(ctx); isPrincipal {
		if !projectScope(p.Scope) || (declared != "" && declared != p.Scope) {
			return threadView{}, errScopeDenied
		}
		scope = p.Scope
	}
	if scope == "" {
		return threadView{all: true}, nil
	}
	db, err := scopeDB(ctx, s, clock)
	if err != nil {
		return threadView{}, err
	}
	ids, err := scopedThreadIDs(ctx, db, scope)
	return threadView{ids: ids}, err
}

// CallerThreadView returns whether the caller on ctx may see a thread id,
// narrowed by declared (empty: no narrowing). The topic verbs filter by it.
func CallerThreadView(ctx context.Context, s Store, clock Clock, declared string) (func(threadID string) bool, error) {
	v, err := resolveView(ctx, s, clock, declared)
	if err != nil {
		return nil, err
	}
	return v.admits, nil
}

// splitScopeParam removes an optional "scope" member from a params object;
// raw without one is returned untouched, so the owner call is unchanged.
func splitScopeParam(raw json.RawMessage) (json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil {
		return raw, "", nil
	}
	member, ok := fields["scope"]
	if !ok {
		return raw, "", nil
	}
	var declared string
	if err := json.Unmarshal(member, &declared); err != nil || declared == "" {
		return nil, "", errScopeParam
	}
	delete(fields, "scope")
	rest, err := json.Marshal(fields)
	return rest, declared, err
}

// scopedListThreads is chat.list_threads filtered by the caller's view.
func (a *Adapter) scopedListThreads(ctx context.Context, raw json.RawMessage) (any, error) {
	rest, declared, err := splitScopeParam(raw)
	if err != nil {
		return nil, err
	}
	v, err := resolveView(ctx, a.store, a.clock, declared)
	if err != nil {
		return nil, err
	}
	res, err := a.handleListThreads(ctx, rest)
	if err != nil || v.all {
		return res, err
	}
	all, _ := res.(listThreadsResult)
	kept := make([]Thread, 0, len(v.ids))
	for _, t := range all.Threads {
		if v.admits(t.ID) {
			kept = append(kept, t)
		}
	}
	return listThreadsResult{Threads: kept}, nil
}

// scopedGetThread is chat.get_thread for a thread in the caller's view only.
func (a *Adapter) scopedGetThread(ctx context.Context, raw json.RawMessage) (any, error) {
	rest, declared, err := splitScopeParam(raw)
	if err != nil {
		return nil, err
	}
	v, err := resolveView(ctx, a.store, a.clock, declared)
	if err != nil {
		return nil, err
	}
	if !v.all {
		var p getThreadParams
		if json.Unmarshal(rest, &p) != nil || !v.admits(p.ThreadID) {
			return nil, errScopeDenied
		}
	}
	return a.handleGetThread(ctx, rest)
}

// scopedAppendThread resolves chat.append_turn's thread before any write: a
// principal continues only a thread in its view, the owner any; a new thread
// is attributed to the caller.
func (a *Adapter) scopedAppendThread(ctx context.Context, params appendTurnParams) (string, error) {
	creating := params.ThreadID == ""
	p, isPrincipal := callerPrincipal(ctx)
	if isPrincipal && !projectScope(p.Scope) {
		return "", errScopeDenied
	}
	if isPrincipal && !creating {
		v, err := resolveView(ctx, a.store, a.clock, "")
		if err != nil {
			return "", err
		}
		if !v.admits(params.ThreadID) {
			return "", errScopeDenied
		}
	} else if !creating {
		_, exists, err := a.store.GetThread(ctx, params.ThreadID)
		if err != nil {
			return "", err
		}
		creating = !exists
	}
	threadID, err := a.resolveAppendThread(ctx, params)
	if err != nil || !creating {
		return threadID, err
	}
	return threadID, a.attributeNew(ctx, threadID)
}

// attributeNew writes the creating caller's scope. It refuses a non-project
// principal itself; on a store without a scope table only the owner proceeds.
func (a *Adapter) attributeNew(ctx context.Context, threadID string) error {
	scope := ownerScope
	if p, ok := callerPrincipal(ctx); ok {
		if !projectScope(p.Scope) {
			return errScopeDenied
		}
		scope = p.Scope
	}
	if _, sqlStore := a.store.(*conversationStore); !sqlStore && scope == ownerScope {
		return nil
	}
	db, err := scopeDB(ctx, a.store, a.clock)
	if err != nil {
		return err
	}
	return runTx(ctx, db, func(tx *sql.Tx) error { return attributeThread(tx, threadID, scope) })
}
