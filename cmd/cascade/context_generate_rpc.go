//go:build !windows

// Purpose: owner-only repository registration and context generation RPC.
// Inputs: strict {repo, check} params, daemon paths, clock and attention queue.
// Outputs: generation results or {drift}; typed errors on invalid input.
// Constraints: only absolute git top-level roots; no MCP tool or prompts.
// SPORT: context.generate (P1-GEN-11c).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"

	"github.com/acamarata/cascade/internal/context/generate"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const contextGenerateMethod = "context.generate"

var _ = rpc.DeclareAccess(contextGenerateMethod, rpc.AccessOwnerOnly)

// contextGenerateDeps holds the daemon's generation collaborators.
type contextGenerateDeps struct {
	Paths     runtime.PathProvider
	Clock     runtime.Clock
	Attention *supervision.Store
	Probe     generate.WorkflowsSupportProbe
}

// generateParams is the closed RPC input vocabulary.
type generateParams struct {
	Repo  string `json:"repo"`
	Check bool   `json:"check"`
}

// decodeGenerateParams rejects unknown fields and trailing JSON values.
func decodeGenerateParams(raw json.RawMessage) (generateParams, error) {
	var p generateParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, cascade.Wrap(cascade.KindInvalidInput, err, "context.generate: params")
	}
	if _, err := dec.Token(); err != io.EOF {
		return p, cascade.New(cascade.KindInvalidInput, "context.generate: params carry trailing data")
	}
	return p, nil
}

// generateRepoRoot validates the caller supplied the git top-level directory.
func generateRepoRoot(ctx context.Context, root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", cascade.New(cascade.KindInvalidInput, "context.generate: repo must be an absolute git top-level directory")
	}
	dirs, err := repoGitCommonDir(ctx, root)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "context.generate: repo must be a git top-level directory")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "context.generate: resolve repo")
	}
	top, err := filepath.EvalSymlinks(dirs.Toplevel)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(top) {
		return "", cascade.New(cascade.KindInvalidInput, "context.generate: repo must equal the git top-level directory")
	}
	return resolved, nil
}

// handle registers the repository before checking or running generation.
func (d contextGenerateDeps) handle(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := decodeGenerateParams(raw)
	if err != nil {
		return nil, err
	}
	root, err := generateRepoRoot(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	inv, err := registerRepository(ctx, d.Paths, d.Clock, root)
	if err != nil {
		return nil, err
	}
	if p.Check {
		drift, err := generate.Check(ctx, root)
		return map[string]any{"drift": drift}, err
	}
	return generate.Run(ctx, root, generate.RunDeps{
		Clock: d.Clock, Attention: d.sink(inv.Repository.ID), Facts: d, Probe: d.Probe,
		Register: func(context.Context, string) (repo.Inventory, error) { return inv, nil },
	})
}

// AcceptedFacts reads through the shared ledger service and its policy filter.
func (d contextGenerateDeps) AcceptedFacts(ctx context.Context, repoID string) ([]repo.AcceptedFact, []string, error) {
	var facts []repo.AcceptedFact
	var denied []string
	err := withRepoDB(ctx, d.Paths, d.Clock, func(db *sql.DB) error {
		svc, err := repo.NewLedgerService(repo.NewStore(db), repo.NewLedgerStore(db))
		if err != nil {
			return err
		}
		facts, denied, err = svc.AcceptedFacts(ctx, repoID)
		return err
	})
	return facts, denied, err
}

// sink persists one attention item per refused target in its repository scope.
func (d contextGenerateDeps) sink(repoID string) generate.AttentionSink {
	return func(ctx context.Context, target, _, _ string) error {
		if d.Attention == nil {
			return cascade.New(cascade.KindUnavailable, "context.generate: supervision queue unavailable")
		}
		_, err := d.Attention.Push(ctx, supervision.AttentionItem{
			Kind: supervision.KindError, SourceRef: "context.generate:" + target,
			ScopeRef: scope.Ref{Kind: scope.ScopeKindProject, ID: repoID},
		})
		return err
	}
}
