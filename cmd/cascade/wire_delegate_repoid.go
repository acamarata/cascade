package main

// Purpose: delegateRepoIDOf is the repo-id binder delegation.Deps.RepoIDOf
//   consumes (T0-ADJUDICATIONS 2026-09-25, contract:delegate-rpc): it maps
//   a delegate's repository root to the context_repository id of record
//   (contract:repo-identity).
// Inputs: the scope GraphStore over the daemon database and the injected
//   GitCommonDirFunc (internal/context.GitCommonDir in production).
// Outputs: the registered repository id, or a typed cascade.Error.
// Constraints: lookup only -- it never calls EnsureRepository,
//   PutRepository or PutRepoPath and never derives an id from a path hash,
//   a repository name or a job id; it is fail-closed: it never returns
//   ("", nil), and a lookup, git or database error is never reported as
//   KindNotFound.
// SPORT: delegate/repo-id/ADD.

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errRepoIDNotRegistered is the sentinel for a canonical repository root
// with no context_repository binding. Callers match it by identity
// through the error chain.
var errRepoIDNotRegistered = cascade.New(cascade.KindNotFound, "delegate: repository not registered")

// delegateRepoIDOf returns the RepoIDOf binder over graph and git. A nil
// graph or nil git yields a binder whose every call fails with
// KindInvalidInput, so the returned func is never nil.
func delegateRepoIDOf(graph *scope.GraphStore, git scope.GitCommonDirFunc) func(ctx context.Context, repoRoot string) (string, error) {
	if graph == nil || git == nil {
		return func(context.Context, string) (string, error) {
			return "", cascade.New(cascade.KindInvalidInput,
				"delegate: repo-id binder requires a graph store and a git resolver")
		}
	}
	return func(ctx context.Context, repoRoot string) (string, error) {
		canonical, err := scope.CanonicalRepoRoot(ctx, repoRoot, git)
		if err != nil {
			return "", err
		}
		rec, ok, err := graph.RepositoryForRoot(ctx, canonical)
		if err != nil {
			return "", cascade.Wrapf(cascade.KindUnavailable, err,
				"delegate: look up repository for %q", canonical)
		}
		if !ok {
			return "", cascade.Wrapf(cascade.KindNotFound, errRepoIDNotRegistered,
				"delegate: repository root %q", canonical)
		}
		if rec.ID == "" {
			return "", cascade.Newf(cascade.KindIntegrity,
				"delegate: repository for %q has an empty id", canonical)
		}
		return rec.ID, nil
	}
}
