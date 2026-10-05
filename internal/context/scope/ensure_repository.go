package scope

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: EnsureRepository is the ONE writer of context_repository rows:
//   it resolves root's CanonicalRepoRoot, returns an
//   already-bound row when one exists, adopts a legacy row that was
//   registered through a pre-canonicalization path (a symlinked ancestor
//   such as ~/Sites -> /mnt/data/Sites or /var -> /private/var) instead
//   of minting a duplicate, and otherwise mints a new row with the same
//   remote+path-hash derivation repo.Scan used before this ticket (ids at
//   HEAD are unchanged for a root that was already canonical).
// Inputs: root (any absolute path inside the repository -- typically a
//   git-root anchor, but EnsureRepository canonicalizes it itself), the
//   repository's remote URL (used only when a new row is minted; an
//   adopted or already-bound row keeps its own recorded remote), and the
//   injected GitCommonDirFunc CanonicalRepoRoot needs.
// Outputs: the single RepositoryRecord that identifies root's canonical
//   repository, or a typed cascade.Error: whatever CanonicalRepoRoot
//   reports, or KindConflict when more than one distinct legacy id
//   resolves to the same canonical root (no write happens in that case).
// Constraints: never mints while an adoptable legacy row exists; the path-hash/id derivation matches internal/repo/scan.go's
//   pre-ticket hashPath/repositoryID exactly, so a root that was already
//   canonical (the common case) keeps its HEAD-derived id bit for bit.
// SPORT: context/repo-identity/ADD.

// EnsureRepository resolves root's canonical path and returns the single
// repository record that identifies it, minting one only when no row --
// canonical or legacy -- already resolves to that canonical root.
func (s *GraphStore) EnsureRepository(ctx context.Context, root, remote string, git GitCommonDirFunc) (RepositoryRecord, error) {
	if git == nil {
		return RepositoryRecord{}, cascade.New(cascade.KindInvalidInput, "context/scope: EnsureRepository requires a non-nil git")
	}
	canonical, err := CanonicalRepoRoot(ctx, root, git)
	if err != nil {
		return RepositoryRecord{}, err
	}

	if rec, ok, err := s.RepositoryForRoot(ctx, canonical); err != nil {
		return RepositoryRecord{}, err
	} else if ok {
		return rec, nil
	}

	adoptedID, err := s.legacyRepositoryID(ctx, canonical)
	if err != nil {
		return RepositoryRecord{}, err
	}
	if adoptedID != "" {
		return s.adoptLegacyRepository(ctx, canonical, adoptedID)
	}

	return s.mintRepository(ctx, canonical, remote)
}

// adoptLegacyRepository binds canonical to an already-registered id
// (found via legacyRepositoryID) instead of minting a new row. It looks
// the repository row up before writing, so a dangling legacy repo_path
// (no repository row behind it) is refused with no write.
func (s *GraphStore) adoptLegacyRepository(ctx context.Context, canonical, id string) (RepositoryRecord, error) {
	rec, ok, err := s.RepositoryByID(ctx, id)
	if err != nil {
		return RepositoryRecord{}, err
	}
	if !ok {
		return RepositoryRecord{}, cascade.Newf(cascade.KindIntegrity,
			"context/scope: adopted repository id %q has no repository row", id)
	}
	if err := s.PutRepoPath(ctx, RepoPathRecord{RootPath: canonical, RepositoryID: id}); err != nil {
		return RepositoryRecord{}, err
	}
	rec.RootPath = canonical
	return rec, nil
}

// mintRepository derives a fresh id from remote+path-hash (repo.Scan's
// pre-ticket derivation, applied to the canonical root) and writes both
// the repository and repo_path rows.
func (s *GraphStore) mintRepository(ctx context.Context, canonical, remote string) (RepositoryRecord, error) {
	pathHash := hashCanonicalRoot(canonical)
	rec := RepositoryRecord{
		ID:       deriveRepositoryID(remote, pathHash),
		RootPath: canonical,
		Remote:   remote,
		PathHash: pathHash,
	}
	if err := s.PutRepository(ctx, rec); err != nil {
		return RepositoryRecord{}, err
	}
	if err := s.PutRepoPath(ctx, RepoPathRecord{RootPath: canonical, RepositoryID: rec.ID}); err != nil {
		return RepositoryRecord{}, err
	}
	return rec, nil
}

// legacyRepositoryID scans every persisted repo_path row and returns the
// single repository id whose own root_path resolves (via EvalSymlinks) to
// canonical -- a row registered before this ticket, through a symlinked
// ancestor. Returns "" when no such row exists. More than one DISTINCT id
// resolving to canonical is a KindConflict: adoption must never guess
// between two legacy identities.
func (s *GraphStore) legacyRepositoryID(ctx context.Context, canonical string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT root_path, repository_id FROM `+tableRepoPath)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "context/scope: scan repo_path for legacy adoption")
	}
	defer func() { _ = rows.Close() }()

	ids := make(map[string]bool)
	for rows.Next() {
		var rootPath, repositoryID string
		if err := rows.Scan(&rootPath, &repositoryID); err != nil {
			return "", cascade.Wrap(cascade.KindUnavailable, err, "context/scope: scan repo_path row")
		}
		resolved, err := filepath.EvalSymlinks(rootPath)
		if err != nil {
			// A stale or no-longer-reachable recorded path is not
			// adoptable; it is not this call's error to report.
			continue
		}
		if filepath.Clean(resolved) == canonical {
			ids[repositoryID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "context/scope: iterate repo_path rows")
	}

	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		for id := range ids {
			return id, nil
		}
	}
	return "", cascade.Newf(cascade.KindConflict,
		"context/scope: canonical root %q resolves from %d distinct legacy repository ids", canonical, len(ids))
}

// RepositoryByID looks up a repository record by its id directly.
// ok=false with a nil error means no repository is registered under id --
// the normal case for an id that was never minted, never itself an error.
func (s *GraphStore) RepositoryByID(ctx context.Context, id string) (RepositoryRecord, bool, error) {
	var rec RepositoryRecord
	err := s.db.QueryRowContext(ctx,
		`SELECT id, remote, path_hash FROM `+tableRepository+` WHERE id = ?`, id).
		Scan(&rec.ID, &rec.Remote, &rec.PathHash)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryRecord{}, false, nil
	}
	if err != nil {
		return RepositoryRecord{}, false, cascade.Wrap(cascade.KindUnavailable, err, "context/scope: lookup repository by id")
	}
	return rec, true, nil
}

// hashCanonicalRoot and deriveRepositoryID exactly replicate
// internal/repo/scan.go's pre-ticket hashPath/repositoryID: a
// deterministic hex digest of the (now canonical) root, and a
// deterministic id from remote+path-hash. Keeping the same algorithm here
// is what keeps a HEAD-derived id unchanged for any root that was already
// canonical (the common, non-symlinked, non-worktree case).
func hashCanonicalRoot(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])
}

func deriveRepositoryID(remote, pathHash string) string {
	sum := sha256.Sum256([]byte(remote + "\x00" + pathHash))
	return hex.EncodeToString(sum[:16])
}
