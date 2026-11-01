//go:build !windows

// Purpose: the context.facts.list, context.facts.propose and
// context.facts.accept handlers over the inferred-fact ledger
// (repo.LedgerService). propose registers the repository, builds the
// admitted content, and runs the one extract intake through the in-process
// conductor door; accept records an operator's elevated accept.
// Inputs: params {repo} (list, propose) or {id} (accept), decoded strictly;
// the verified elevation witness for accept; contextFactsDeps.
// Outputs: {facts}, {proposed, accepted, rejected, refused} and {fact}.
// Constraints: all three verbs are owner-only; any other param is
// KindInvalidInput, so neither a tier nor accepted_by ever comes from params;
// the propose tier is conductor.TaskClassExtract's SensitivityDefault read at
// call time; accept touches the ledger only after rpc.VerifiedElevationFrom
// shows a witness for context.facts.accept over these exact params.
// SPORT: cmd/cascade context facts RPCs (P1-GEN-02).
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// init adds the context.facts.accept row to the elevation table: an accept
// always needs a fresh attestation.
func init() {
	rpc.RegisterElevatedVerb(contextFactsAcceptMethod, rpc.ElevationRule{Always: true})
}

var (
	_ = rpc.DeclareAccess(contextFactsListMethod, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(contextFactsProposeMethod, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(contextFactsAcceptMethod, rpc.AccessOwnerOnly)
)

// contextFactsDeps is what the three handlers read.
type contextFactsDeps struct {
	Paths   runtime.PathProvider
	Clock   runtime.Clock
	Exec    provider.ModelExecutor
	Admit   func(ctx context.Context, root string, rels []string) ([]repo.Admission, error)
	Tracked func(ctx context.Context, root string) ([]string, error)
}

// factsRepoParams is the {repo} params of list and propose.
type factsRepoParams struct {
	Repo string `json:"repo"`
}

// factsAcceptParams is the {id} params of accept.
type factsAcceptParams struct {
	ID string `json:"id"`
}

// factsProposeResult is the propose reply; Refused counts refused candidates.
type factsProposeResult struct {
	Proposed []repo.InferredFact `json:"proposed"`
	Accepted []repo.InferredFact `json:"accepted"`
	Rejected []repo.InferredFact `json:"rejected"`
	Refused  int                 `json:"refused"`
}

// mountContextFacts registers the three verbs over d.
func mountContextFacts(reg *rpc.Registry, d contextFactsDeps) {
	reg.Register(contextFactsListMethod, d.list)
	reg.Register(contextFactsProposeMethod, d.propose)
	reg.Register(contextFactsAcceptMethod, d.accept)
}

// decodeFactsParams decodes raw into out, refusing unknown fields and
// trailing data.
func decodeFactsParams(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "context.facts: params")
	}
	if _, err := dec.Token(); err != io.EOF {
		return cascade.New(cascade.KindInvalidInput, "context.facts: params carry trailing data")
	}
	return nil
}

// extractTier is the extract task class's default sensitivity.
func extractTier() (provider.SensitivityTier, error) {
	for _, row := range conductor.TaskClasses() {
		if row.Class == string(conductor.TaskClassExtract) {
			return row.SensitivityDefault, nil
		}
	}
	return provider.SensitivityLocalOnly, cascade.New(cascade.KindInternal, "context.facts.propose: no extract task class")
}

// list returns every stored version of every fact for {repo}.
func (d contextFactsDeps) list(ctx context.Context, raw json.RawMessage) (any, error) {
	var p factsRepoParams
	if err := decodeFactsParams(raw, &p); err != nil {
		return nil, err
	}
	canonical, err := canonicalRepoRoot(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	facts := []repo.InferredFact{}
	err = withRepoDB(ctx, d.Paths, d.Clock, func(db *sql.DB) error {
		rec, ok, err := scope.NewGraphStore(db).RepositoryForRoot(ctx, canonical)
		if err != nil || !ok {
			return err
		}
		svc, err := repo.NewLedgerService(repo.NewStore(db), repo.NewLedgerStore(db))
		if err != nil {
			return err
		}
		rows, err := svc.List(ctx, rec.ID)
		if err != nil {
			return err
		}
		facts = append(facts, rows...)
		return nil
	})
	return map[string]any{"facts": facts}, err
}

// propose registers {repo}, admits its candidate files, runs the extract
// intake once and records the proposals.
func (d contextFactsDeps) propose(ctx context.Context, raw json.RawMessage) (any, error) {
	var p factsRepoParams
	if err := decodeFactsParams(raw, &p); err != nil {
		return nil, err
	}
	tier, err := extractTier()
	if err != nil {
		return nil, err
	}
	intake, err := repo.NewExtractIntake(d.Exec)
	if err != nil {
		return nil, err
	}
	if r, ok := d.Exec.(interface{ Ready() error }); ok {
		if err := r.Ready(); err != nil {
			return nil, err
		}
	}
	inv, err := registerRepository(ctx, d.Paths, d.Clock, p.Repo)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalRepoRoot(ctx, p.Repo)
	if err != nil {
		return nil, err
	}
	content, refused, err := admittedFactContent(ctx, d, canonical)
	if err != nil {
		return nil, err
	}
	out := factsProposeResult{Refused: refused}
	err = withRepoDB(ctx, d.Paths, d.Clock, func(db *sql.DB) error {
		return recordProposals(ctx, db, intake, inv.Repository.ID, content, tier, &out)
	})
	return out, err
}

// recordProposals runs the intake over the stored inventory and records it.
func recordProposals(ctx context.Context, db *sql.DB, intake *repo.ExtractIntake, repoID string, content []repo.Content, tier provider.SensitivityTier, out *factsProposeResult) error {
	store := repo.NewStore(db)
	inv, ok, err := store.Get(ctx, repoID)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.Newf(cascade.KindNotFound, "context.facts.propose: no stored inventory for %s", repoID)
	}
	props, err := intake.Propose(ctx, inv, content, tier)
	if err != nil {
		return err
	}
	svc, err := repo.NewLedgerService(store, repo.NewLedgerStore(db))
	if err != nil {
		return err
	}
	res, err := svc.Record(ctx, repoID, props)
	out.Proposed, out.Accepted, out.Rejected = res.Proposed, res.Accepted, res.Rejected
	return err
}

// accept records an operator accept of {id}; it needs the witness first.
func (d contextFactsDeps) accept(ctx context.Context, raw json.RawMessage) (any, error) {
	witness, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok || witness.Method() != contextFactsAcceptMethod || witness.ParamsHash() != rpc.ParamsHash(raw) {
		return nil, cascade.New(cascade.KindElevationRequired, "context.facts.accept needs a verified elevation for this call")
	}
	var p factsAcceptParams
	if err := decodeFactsParams(raw, &p); err != nil {
		return nil, err
	}
	var fact repo.InferredFact
	err := withRepoDB(ctx, d.Paths, d.Clock, func(db *sql.DB) error {
		svc, err := repo.NewLedgerService(repo.NewStore(db), repo.NewLedgerStore(db))
		if err != nil {
			return err
		}
		fact, err = svc.AcceptByOperator(ctx, p.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"fact": fact}, nil
}

// Propose content bounds: one candidate and the whole request.
const (
	factContentFileCap  = 32 << 10
	factContentTotalCap = 128 << 10
)

// factCandidate reports whether a tracked path is on the closed candidate
// list: root-level README*, go.mod, package.json, Cargo.toml, pyproject.toml,
// Makefile, or .github/workflows/*.yml.
func factCandidate(rel string) bool {
	switch rel {
	case "go.mod", "package.json", "Cargo.toml", "pyproject.toml", "Makefile":
		return true
	}
	if !strings.Contains(rel, "/") {
		return strings.HasPrefix(rel, "README")
	}
	return path.Dir(rel) == ".github/workflows" && path.Ext(rel) == ".yml"
}

// admittedFactContent admits the tracked candidates of root and returns their
// Admission.Content, capped, plus the count of refused candidates. A path off
// the candidate list is never handed to the admission guard, so never opened.
func admittedFactContent(ctx context.Context, d contextFactsDeps, root string) ([]repo.Content, int, error) {
	tracked, err := d.Tracked(ctx, root)
	if err != nil {
		return nil, 0, err
	}
	var rels []string
	for _, rel := range tracked {
		if factCandidate(rel) && len(rels) < repo.MaxAdmitBatch {
			rels = append(rels, rel)
		}
	}
	if len(rels) == 0 {
		return nil, 0, nil
	}
	adm, err := d.Admit(ctx, root, rels)
	if err != nil {
		return nil, 0, err
	}
	var out []repo.Content
	refused, budget := 0, factContentTotalCap
	for _, a := range adm {
		if !a.Admitted {
			refused++
			continue
		}
		if budget <= 0 {
			continue
		}
		b := capFactContent(a.Content, min(factContentFileCap, budget))
		budget -= len(b)
		out = append(out, repo.Content{Rel: a.Rel, Bytes: b})
	}
	return out, refused, nil
}

// capFactContent truncates b to at most limit bytes at a line boundary and
// notes the truncation.
func capFactContent(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	cut := b[:limit]
	if i := bytes.LastIndexByte(cut, '\n'); i >= 0 {
		cut = cut[:i+1]
	}
	note := fmt.Sprintf("\n[cascade: truncated to %d of %d bytes]\n", len(cut), len(b))
	return append(append([]byte(nil), cut...), note...)
}
