package conversation

// Purpose: a chat thread's PROJECT, recorded once when the thread is created
//   and never changed. Who may read a thread is thread_scope's question
//   (scope_attr.go); where the thread belongs is this table's, so a recall
//   leg can narrow to one project without touching the attribution contract.
// Inputs: the creating call's Caller on ctx, the cwd that call carried, and
//   an optional resolver (SetProjectResolver) that maps a cwd to a project ref.
// Outputs: at most one conversation_thread_project row per thread, written in
//   the SAME transaction as the thread (CreateThreadProject,
//   AppendFirstTurnProject). project_ref "" is the general bucket (a cwd that
//   resolved to no project), distinct from no row at all.
// Constraints: NO ROW MEANS UNSCOPED, and unscoped is never widened: a nil
//   resolver, a resolver error, an empty cwd, a caller that is neither the
//   owner CLI nor a project-scoped principal, all write no row, and a project
//   narrowed search joins on the row so such a thread matches nothing.
//   A principal's project is its authenticated Scope; the cwd it may also send
//   is ignored. Only the creating call reads a cwd: an append to an existing
//   thread never adds or changes a row. SetThreadProject is insert-once.
// SPORT: internal.conversation.project/ADDED (P1-CPA-06, contract:thread-scope).

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// tableThreadProject holds one row per thread whose creating call resolved a
// project (or the general bucket).
const tableThreadProject = "conversation_thread_project"

// ThreadProjectStore is the thread-project half of Store, declared beside its
// implementation.
type ThreadProjectStore interface {
	// SetThreadProject records threadID's project ref, insert-once: the first
	// write wins and a later one (the same ref or another) changes nothing.
	// projectRef "" is the general bucket. An empty threadID is ErrInvalidRecord.
	SetThreadProject(ctx context.Context, threadID, projectRef string) error
	// ThreadProject reads threadID's project ref; ok=false means unscoped.
	ThreadProject(ctx context.Context, threadID string) (projectRef string, ok bool, err error)
	// CreateThreadProject is CreateThread plus the thread's project row,
	// all in ONE transaction: project nil writes no row, otherwise *project
	// is its ref. A project-store error is KindUnavailable and rolls back the
	// thread, its marker and its attribution too.
	CreateThreadProject(ctx context.Context, threadID string, tier provider.SensitivityTier,
		attribution string, project *string, createdAt int64) error
	// AppendFirstTurnProject is AppendFirstTurn plus the thread's project
	// row, in the same transaction and with the same nil rule.
	AppendFirstTurnProject(ctx context.Context, tier provider.SensitivityTier, turn Turn,
		segments []Segment, project *string) error
}

// projectStep is MigrationSet's step for the project table. No foreign key,
// the privacyStep precedent: the row is written in the transaction that
// creates the thread, and a stray row for an id that never gets a thread is
// inert (a search joins from the segment index, which only live threads fill).
func projectStep() migrate.MigrationStep {
	return migrate.MigrationStep{
		Kind:        migrate.StepCreateTable,
		Description: "conversation_thread_project: the project a thread belongs to, written once at creation",
		Table: &migrate.TableDef{
			Name:    tableThreadProject,
			Columns: []migrate.ColumnDef{id("thread_id"), text("project_ref")},
		},
	}
}

// setThreadProjectTx inserts threadID's project row inside tx. Insert-once:
// an existing row is left exactly as it is. A failure is KindUnavailable.
func setThreadProjectTx(ctx context.Context, tx *sql.Tx, threadID, projectRef string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableThreadProject+` (thread_id, project_ref) VALUES (?, ?) ON CONFLICT(thread_id) DO NOTHING`,
		threadID, projectRef)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "conversation: record thread project")
	}
	return nil
}

// writeProjectTx writes project's row inside tx, or nothing for nil.
func writeProjectTx(ctx context.Context, tx *sql.Tx, threadID string, project *string) error {
	if project == nil {
		return nil
	}
	return setThreadProjectTx(ctx, tx, threadID, *project)
}

// SetThreadProject implements ThreadProjectStore.
func (s *conversationStore) SetThreadProject(ctx context.Context, threadID, projectRef string) error {
	if threadID == "" {
		return ErrInvalidRecord
	}
	return runTx(ctx, s.db, func(tx *sql.Tx) error { return setThreadProjectTx(ctx, tx, threadID, projectRef) })
}

// ThreadProject implements ThreadProjectStore.
func (s *conversationStore) ThreadProject(ctx context.Context, threadID string) (string, bool, error) {
	var ref string
	err := s.db.QueryRowContext(ctx,
		`SELECT project_ref FROM `+tableThreadProject+` WHERE thread_id = ?`, threadID).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, cascade.Wrap(cascade.KindUnavailable, err, "conversation: read thread project")
	}
	return ref, true, nil
}

// SetProjectResolver wires the owner-CLI cwd resolver: f maps a cwd to a
// project ref, "" with a nil error being the general bucket and any error
// leaving the thread unscoped. A nil f (the default, and always on Windows)
// records no project for an owner thread.
func (a *Adapter) SetProjectResolver(f func(ctx context.Context, in scope.ResolveInput) (string, error)) {
	a.projectResolver = f
}

// projectFor returns the project a creating call records, or nil for none.
// A principal's project is its Scope (never the cwd); the owner CLI's is its
// cwd resolved; anything else, and every failure, is nil (unscoped).
func (a *Adapter) projectFor(ctx context.Context, cwd string) *string {
	c, ok := principal.CallerFrom(ctx)
	switch {
	case !ok:
		return nil
	case c.Principal != nil:
		if !projectScope(c.Principal.Scope) {
			return nil
		}
		ref := c.Principal.Scope
		return &ref
	case c.OwnerCLI:
		return a.ownerProject(ctx, cwd)
	}
	return nil
}

// ownerProject resolves the owner CLI's cwd. A nil resolver, an empty cwd or
// a resolver error is unscoped, never a guess.
func (a *Adapter) ownerProject(ctx context.Context, cwd string) *string {
	if a.projectResolver == nil || cwd == "" {
		return nil
	}
	ref, err := a.projectResolver(ctx, scope.ResolveInput{Cwd: cwd})
	if err != nil {
		return nil
	}
	return &ref
}
