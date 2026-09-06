package scope

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: GraphStore is the persisted scope-graph CRUD surface over the
//   four ScopeMigrationSet tables: node upsert, edge upsert (with R-21.157
//   cycle rejection over the member_of/parent classes at build time),
//   repository/repo_path upsert, and the direct-neighbor reads resolver.go
//   composes into ResolveSessionScope and CandidateScopeRefs.
// Inputs: an open *sql.DB already migrated via ApplyScopeSchema.
// Outputs: typed A-T7 errors for malformed, missing, or storage-failure
//   paths; never a bare *sql.Rows leak or a silent nil success.
// Constraints: every edge kind is validated (model.go's ValidateEdgeKind)
//   before it reaches SQL; PutEdge REJECTS a member_of edge that would
//   close a cycle -- route edges (depends_on/shares_context_with) are not
//   cycle-checked, since a dependency or context-sharing relationship is
//   not a containment hierarchy and R-21.157 scopes cycle rejection to
//   "the parent/member classes" only.
// SPORT: context/scope-graph/ADD.

// GraphStore is the persisted scope-graph CRUD surface.
type GraphStore struct {
	db *sql.DB
}

// NewGraphStore wraps db. db must already carry the ScopeMigrationSet
// tables (ApplyScopeSchema).
func NewGraphStore(db *sql.DB) *GraphStore {
	return &GraphStore{db: db}
}

// PutRepository upserts one repository record by ID.
func (s *GraphStore) PutRepository(ctx context.Context, rec RepositoryRecord) error {
	if rec.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "context/scope: repository id is required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableRepository+` (id, remote, path_hash) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET remote=excluded.remote, path_hash=excluded.path_hash`,
		rec.ID, rec.Remote, rec.PathHash)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "context/scope: put repository")
	}
	return nil
}

// PutRepoPath upserts one local filesystem anchor -> repository binding.
func (s *GraphStore) PutRepoPath(ctx context.Context, rec RepoPathRecord) error {
	if rec.RootPath == "" || rec.RepositoryID == "" {
		return cascade.New(cascade.KindInvalidInput, "context/scope: repo_path root_path and repository_id are required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableRepoPath+` (root_path, repository_id) VALUES (?, ?)
		 ON CONFLICT(root_path) DO UPDATE SET repository_id=excluded.repository_id`,
		rec.RootPath, rec.RepositoryID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "context/scope: put repo_path")
	}
	return nil
}

// RepositoryForRoot looks up the repository bound to rootPath (the
// resolved git-root anchor). ok=false with a nil error means "no binding
// recorded for this root" -- the normal, expected case for an unregistered
// working tree, never itself an error.
func (s *GraphStore) RepositoryForRoot(ctx context.Context, rootPath string) (RepositoryRecord, bool, error) {
	var repoID string
	err := s.db.QueryRowContext(ctx,
		`SELECT repository_id FROM `+tableRepoPath+` WHERE root_path = ?`, rootPath).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryRecord{}, false, nil
	}
	if err != nil {
		return RepositoryRecord{}, false, cascade.Wrap(cascade.KindUnavailable, err, "context/scope: lookup repo_path")
	}
	var rec RepositoryRecord
	rec.ID = repoID
	err = s.db.QueryRowContext(ctx,
		`SELECT id, remote, path_hash FROM `+tableRepository+` WHERE id = ?`, repoID).
		Scan(&rec.ID, &rec.Remote, &rec.PathHash)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryRecord{}, false, cascade.Newf(cascade.KindIntegrity,
			"context/scope: repo_path %q references missing repository %q", rootPath, repoID)
	}
	if err != nil {
		return RepositoryRecord{}, false, cascade.Wrap(cascade.KindUnavailable, err, "context/scope: lookup repository")
	}
	rec.RootPath = rootPath
	return rec, true, nil
}

// PutScope upserts one scope graph node.
func (s *GraphStore) PutScope(ctx context.Context, rec ScopeGraphRecord) error {
	if !rec.Ref.Kind.Valid() || rec.Ref.ID == "" {
		return cascade.Newf(cascade.KindInvalidInput, "context/scope: invalid scope ref %+v", rec.Ref)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableScope+` (kind, id, display_name) VALUES (?, ?, ?)
		 ON CONFLICT(kind, id) DO UPDATE SET display_name=excluded.display_name`,
		string(rec.Ref.Kind), rec.Ref.ID, rec.DisplayName)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "context/scope: put scope")
	}
	return nil
}

// PutEdge validates edge.Kind against the closed EdgeKind vocabulary,
// REJECTS a member_of edge that would close a cycle (R-21.157: cycles are
// rejected at graph BUILD time, never discovered at query time), and
// upserts it.
func (s *GraphStore) PutEdge(ctx context.Context, edge ScopeEdge) error {
	if err := ValidateEdgeKind(edge.Kind); err != nil {
		return err
	}
	if !edge.From.Kind.Valid() || edge.From.ID == "" || !edge.To.Kind.Valid() || edge.To.ID == "" {
		return cascade.Newf(cascade.KindInvalidInput, "context/scope: invalid edge endpoints %+v", edge)
	}
	class, ok := EdgeClassFor(edge.Kind)
	if !ok {
		return cascade.Newf(cascade.KindInvalidInput, "context/scope: edge kind %q has no traversal class", edge.Kind)
	}
	if class == EdgeClassMember {
		closes, err := s.wouldCloseMemberCycle(ctx, edge.From, edge.To)
		if err != nil {
			return err
		}
		if closes {
			return cascade.Newf(cascade.KindInvalidInput,
				"context/scope: member_of edge %+v -> %+v would close a cycle", edge.From, edge.To)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableScopeEdge+` (from_kind, from_id, to_kind, to_id, edge_kind) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(from_kind, from_id, to_kind, to_id) DO UPDATE SET edge_kind=excluded.edge_kind`,
		string(edge.From.Kind), edge.From.ID, string(edge.To.Kind), edge.To.ID, string(edge.Kind))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "context/scope: put edge")
	}
	return nil
}

// wouldCloseMemberCycle reports whether adding a member_of edge from->to
// would close a cycle: true when to can already reach from via existing
// member_of edges (a cycle closes the moment the new edge's target can
// already walk back to the new edge's source). A self-edge (from == to)
// is always a one-node cycle.
func (s *GraphStore) wouldCloseMemberCycle(ctx context.Context, from, to ScopeRef) (bool, error) {
	if from == to {
		return true, nil
	}
	visited := map[ScopeRef]bool{to: true}
	frontier := []ScopeRef{to}
	for len(frontier) > 0 {
		next := frontier[0]
		frontier = frontier[1:]
		targets, err := s.EdgeTargets(ctx, next, EdgeKindMemberOf)
		if err != nil {
			return false, err
		}
		for _, t := range targets {
			if t == from {
				return true, nil
			}
			if !visited[t] {
				visited[t] = true
				frontier = append(frontier, t)
			}
		}
	}
	return false, nil
}

// ParentScopes returns the direct member_of targets of ref: the scopes ref
// declares itself a member of. Named "parent" because member_of is the
// containment direction resolver.go walks upward through (project ->
// product/workspace).
func (s *GraphStore) ParentScopes(ctx context.Context, ref ScopeRef) ([]ScopeRef, error) {
	return s.EdgeTargets(ctx, ref, EdgeKindMemberOf)
}

// EdgeTargets returns the direct targets of every persisted edge FROM ref
// whose kind equals edgeKind, in insertion-stable order (ORDER BY the
// composite key). Returns an empty, non-nil slice (never an error) when
// ref has no such edges.
func (s *GraphStore) EdgeTargets(ctx context.Context, ref ScopeRef, edgeKind EdgeKind) ([]ScopeRef, error) {
	if err := ValidateEdgeKind(edgeKind); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT to_kind, to_id FROM `+tableScopeEdge+` WHERE from_kind = ? AND from_id = ? AND edge_kind = ? ORDER BY to_kind, to_id`,
		string(ref.Kind), ref.ID, string(edgeKind))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "context/scope: query edge targets")
	}
	defer rows.Close()
	out := make([]ScopeRef, 0)
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "context/scope: scan edge target")
		}
		out = append(out, ScopeRef{Kind: ScopeKind(kind), ID: id})
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "context/scope: iterate edge targets")
	}
	return out, nil
}
