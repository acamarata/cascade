package scope

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: ResolveSessionScope walks the fixed R-16.3 order (git root ->
//   repository -> project/product/workspace via the persisted graph ->
//   branch -> active task -> session) and CandidateScopeRefs applies the
//   R-21.157 traversal table to compute the deny-by-default candidate set.
// Inputs: a GraphStore, a GitRootFunc (the S-08.T1 anchor -- see
//   defaultGitRoot's doc comment for why this package carries its own
//   copy), and caller-supplied user/machine/branch/task/session/
//   explicit_overrides values this package never invents.
// Outputs: a SessionScope (kind `general` on an unresolved cwd, never an
//   error for that case -- R-16.3: "this is a successful restricted
//   result"), or a *cascade.Error for a genuine storage failure.
// Constraints: no implicit graph edge, no authorization widening, no
//   cross-project name matching. CandidateScopeRefs reads direction and
//   transitivity ONLY from traversal.go's Traversal table.
// SPORT: context/session-scope-resolver/ADD.

// GitRootFunc resolves cwd's git root, matching the S-08.T1 anchor
// contract (git rev-parse --show-toplevel, falling back to cwd on any
// failure -- never an error). This package declares only the function
// TYPE, never a concrete "os/exec"-based implementation: the A-T2 egress
// process-spawn allowlist (internal/build/egress_allow.go, outside this
// ticket's files_scope) admits "cmd/cascade" and "internal/context" as
// process spawners, but not internal/context/scope -- adding a new
// entry there is not this ticket's to make. The real implementation
// (cmd/cascade/context_scope.go's cliGitRoot, `git rev-parse
// --show-toplevel`, mirroring internal/context/discover.go's unexported
// gitRoot byte for byte) lives at the composition root instead, which the
// allowlist already admits; every caller here injects a GitRootFunc.
// Tests inject a fake so no unit test spawns a real git subprocess.
type GitRootFunc func(ctx context.Context, cwd string) string

// ResolveDeps carries this package's injected dependencies.
type ResolveDeps struct {
	Store   *GraphStore
	GitRoot GitRootFunc
}

// ResolveInput carries every caller-supplied value ResolveSessionScope
// attaches as-is: it resolves NONE of these from the graph. Cwd is the
// only value the resolution order itself interprets (via GitRoot).
type ResolveInput struct {
	Cwd               string
	User              string
	Machine           string
	Branch            string
	Task              string
	Session           string
	ExplicitOverrides string
}

// ResolveSessionScope implements the fixed R-16.3 order. An unresolved
// cwd (no repository bound to the resolved git root) returns a
// SUCCESSFUL ScopeKindGeneral result -- never an error, never a fallback
// to an unscoped/global query.
func ResolveSessionScope(ctx context.Context, deps ResolveDeps, in ResolveInput) (SessionScope, error) {
	if deps.Store == nil {
		return SessionScope{}, cascade.New(cascade.KindInvalidInput, "context/scope: ResolveSessionScope requires a non-nil Store")
	}
	if deps.GitRoot == nil {
		return SessionScope{}, cascade.New(cascade.KindInvalidInput, "context/scope: ResolveSessionScope requires a non-nil GitRoot")
	}
	root := deps.GitRoot(ctx, in.Cwd)

	repo, ok, err := deps.Store.RepositoryForRoot(ctx, root)
	if err != nil {
		return SessionScope{}, err
	}
	if !ok {
		return SessionScope{
			Kind:              ScopeKindGeneral,
			User:              in.User,
			Machine:           in.Machine,
			Cwd:               in.Cwd,
			Session:           in.Session,
			ExplicitOverrides: in.ExplicitOverrides,
		}, nil
	}

	projectRef := ScopeRef{Kind: ScopeKindProject, ID: repo.ID}
	parents, err := deps.Store.ParentScopes(ctx, projectRef)
	if err != nil {
		return SessionScope{}, err
	}
	var workspace, product string
	for _, p := range parents {
		switch p.Kind {
		case ScopeKindWorkspace:
			workspace = p.ID
		case ScopeKindProduct:
			product = p.ID
		}
	}

	pkgPath := ""
	if rel, relErr := filepath.Rel(root, in.Cwd); relErr == nil && rel != "." {
		pkgPath = filepath.ToSlash(rel)
	}

	repoCopy := repo
	repoCopy.RootPath = root
	return SessionScope{
		Kind:              ScopeKindSession,
		User:              in.User,
		Machine:           in.Machine,
		Cwd:               in.Cwd,
		Workspace:         workspace,
		Product:           product,
		Project:           repo.ID,
		Repository:        &repoCopy,
		PackagePath:       pkgPath,
		Branch:            in.Branch,
		Task:              in.Task,
		Session:           in.Session,
		ExplicitOverrides: in.ExplicitOverrides,
	}, nil
}

// ScopeChain returns s's own resolved scope chain, in outbound EdgeClassParent
// order (session -> task -> project -> workspace/product), skipping any
// unresolved link. A ScopeKindGeneral scope has an empty chain: it has no
// graph membership to traverse from, by construction.
func ScopeChain(s SessionScope) []ScopeRef {
	if s.Kind == ScopeKindGeneral {
		return nil
	}
	var chain []ScopeRef
	if s.Session != "" {
		chain = append(chain, ScopeRef{Kind: ScopeKindSession, ID: s.Session})
	}
	if s.Task != "" {
		chain = append(chain, ScopeRef{Kind: ScopeKindTask, ID: s.Task})
	}
	if s.Project != "" {
		chain = append(chain, ScopeRef{Kind: ScopeKindProject, ID: s.Project})
	}
	if s.Workspace != "" {
		chain = append(chain, ScopeRef{Kind: ScopeKindWorkspace, ID: s.Workspace})
	}
	if s.Product != "" {
		chain = append(chain, ScopeRef{Kind: ScopeKindProduct, ID: s.Product})
	}
	return chain
}

// routeEdgeKinds are the two persisted EdgeKinds EdgeClassRoute covers
// (traversal.go's EdgeClassFor mapping, read in reverse here).
var routeEdgeKinds = []EdgeKind{EdgeKindDependsOn, EdgeKindSharesContextWith}

// CandidateScopeRefs returns the deny-by-default candidate set: chain's
// own deduplicated refs, plus the distinct direct targets of explicit
// route edges sourced FROM a ref in chain -- and nothing else. Direction
// and transitivity come from Traversal exclusively; a (kind, route) pair
// absent from that table contributes no targets for that kind, matching
// the "no permissive default" rule.
func CandidateScopeRefs(ctx context.Context, store *GraphStore, chain []ScopeRef) ([]ScopeRef, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "context/scope: CandidateScopeRefs requires a non-nil store")
	}
	seen := make(map[ScopeRef]bool, len(chain))
	out := make([]ScopeRef, 0, len(chain))
	for _, ref := range chain {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, ref := range chain {
		rule, ok := Traversal(ref.Kind, EdgeClassRoute)
		if !ok || rule.Direction == DirectionInbound {
			continue
		}
		for _, ek := range routeEdgeKinds {
			targets, err := store.EdgeTargets(ctx, ref, ek)
			if err != nil {
				return nil, err
			}
			for _, t := range targets {
				if !seen[t] {
					seen[t] = true
					out = append(out, t)
				}
			}
		}
	}
	return out, nil
}
