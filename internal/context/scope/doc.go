// Package scope implements the R-16.3 session-scope model: SessionScope
// resolution from a git-root anchor plus the persisted context-domain
// scope graph (the four tables scope, scope_edge, repository, repo_path),
// the R-21.157 closed traversal table, and deny-by-default candidate
// selection.
//
// Purpose: answer "what scope is this session in, and what other scopes
//
//	may it see" without ever inventing an implicit relationship or
//	widening authorization — every visible scope traces to either the
//	session's own resolved chain or an explicitly persisted edge.
//
// Inputs: a working directory (routed through the S-08.T1 git-root
//
//	anchor), an open *sql.DB already bootstrapped into the `context`
//	storage domain (B/S-02.T2), and caller-supplied user/machine/branch/
//	task/session identifiers this package never invents on its own.
//
// Outputs: SessionScope, Ref, and the CandidateScopeRefs slice, all
//
//	pkg/cascade-typed on every error path.
//
// Constraints: no fifth table, no parallel sessions domain (sessions stay
//
//	in the `sessions` domain per R-16.3); PutEdge rejects any edge that
//	would close a cycle over the parent/member classes at BUILD time
//	(R-21.157); Traversal has no permissive default — an unlisted (kind,
//	edge) pair denies. No bare time.Now; no os.Stdout/Stderr. Every file
//	in this package stays under the 300-line cap.
//
// # Canonical repository root
//
// context_repository is the repository identity of record for GEN and CI:
// callers never key a repository by a raw path. CanonicalRepoRoot
// (canonical_root.go) is the ONE rule for turning a working directory
// into that identity's anchor. (0) A bare common dir (core.bare=true in
// its own config) is the root, for the bare dir and for every worktree
// of it, so `git clone --bare src proj/.git` plus `git worktree add
// proj/main` is one identity. Otherwise, in this fixed order: (1) a
// common dir named `.git` means its parent; (2) a set core.worktree, made
// absolute against the common dir
// (submodules, `--separate-git-dir` layouts that set it), names the
// root; (3) only in the main worktree (git dir == common dir) the
// `--show-toplevel` value is the root; (4) anything else refuses with
// KindInvalidInput repo_root_ambiguous, never a guess. The chosen root is
// then passed through filepath.EvalSymlinks, so a main checkout, a
// `git worktree add` worktree of it, and a path reached through a
// symlinked ancestor (e.g. `~/Sites` -> `/mnt/data/Sites`) resolve to
// one value, while two submodules of one super-repository stay two.
// Never resolve through `--show-toplevel` alone: it returns a linked
// worktree's own toplevel, not its main checkout's.
//
// EnsureRepository (ensure_repository.go) is the ONE writer of
// context_repository rows: it canonicalizes first, returns an
// already-bound row when one exists, adopts a legacy row registered
// through a pre-canonicalization path instead of minting a duplicate (a
// row written before this rule existed is found, never re-minted), and
// only otherwise derives a fresh id via the same remote+path-hash
// algorithm repo.Scan used before this rule — so an already-canonical
// root's id never changes at HEAD.
//
// SPORT: context/scope-model + context/scope-graph +
//
//	context/session-scope-resolver + context/repo-identity (ADD, per T-4
//	sport_updates).
package scope
