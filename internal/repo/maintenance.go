package repo

// Purpose: apply a batch of changed repository paths through ONE journaled,
//   crash-safe pipeline: admission, detector family, scoped Go graph, FTS,
//   vectors, stale-fact rejection, then the cursor (contract:repo-maintenance).
// Inputs: MaintenanceDeps (stores, an injected IndexLeg and Attention, a
//   Clock) and Batches of slash repo-relative paths.
// Outputs: a BatchReport per Apply/Replay; effects in the stores and the index.
// Constraints: every path is admitted (AdmitBatch, in slices of
//   MaxAdmitBatch) before any read of its bytes; an intent row precedes the
//   legs and clears only in the transaction that writes the path hash; the
//   cursor advances once, last, and only when every path applied; there is no
//   full reindex and no full detector sweep on this path; internal/repo never
//   imports retrieval, fleet or daemon (IndexLeg and Attention are injected).
// SPORT: repo/maintenance-pipeline/ADD (contract:repo-maintenance, P1-GEN-04).

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// IndexLeg is the index side the pipeline writes through; the retrieval
// package binds it elsewhere.
type IndexLeg interface {
	WriteFTS(ctx context.Context, repoID, rel string, content []byte) (chunkIDs []string, err error)
	// DeleteChunks removes FTS rows and vectors; absent ids are no error.
	DeleteChunks(ctx context.Context, repoID string, chunkIDs []string) error
	WriteVector(ctx context.Context, repoID, rel string, chunkIDs []string, content []byte) (pending bool, err error)
	// WriteGraph replaces the repository's graph-corpus rows.
	WriteGraph(ctx context.Context, repoID string, g SymbolGraph) error
}

// Attention raises an owner-visible note; bound to supervision elsewhere.
type Attention func(ctx context.Context, sourceRef, text string) error

// MaintenanceDeps wires one repository's pipeline. Every field is required.
type MaintenanceDeps struct {
	Root, RepositoryID string
	Journal            *WatchJournal
	Inventory          *Store
	Graph              *GraphStore
	Ledger             *LedgerStore
	Index              IndexLeg
	Attention          Attention
	Clock              runtime.Clock
}

// Batch is one set of changed paths from a change source.
type Batch struct {
	Changed         []string // slash repo-relative
	FSSeq           uint64
	Commit, TreeOID string
}

// BatchReport sorts each path of a run by outcome. Invalidated holds fact ids.
type BatchReport struct {
	Applied, Refused, Unchanged, Failed, VectorPending, Invalidated []string
}

// afterLeg is a test seam: it runs after each leg is journaled.
var afterLeg = func(_ string, _ int) {}

// Maintenance is the pipeline for one repository.
type Maintenance struct{ d MaintenanceDeps }

// NewMaintenance validates d; any nil or empty field is KindInvalidInput.
func NewMaintenance(d MaintenanceDeps) (*Maintenance, error) {
	switch {
	case d.Root == "", d.RepositoryID == "":
		return nil, cascade.New(cascade.KindInvalidInput, "repo: maintenance requires a root and a repository id")
	case d.Journal == nil, d.Inventory == nil, d.Graph == nil, d.Ledger == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "repo: maintenance requires a journal, inventory, graph and ledger store")
	case d.Index == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "repo: maintenance requires an IndexLeg")
	case d.Attention == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "repo: maintenance requires an Attention func")
	case d.Clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "repo: maintenance requires a Clock")
	}
	return &Maintenance{d: d}, nil
}

// Apply replays any unfinished intent first, then applies b path by path in
// sorted order. A leg error on one path reports it Failed and continues; the
// returned error is only for journal or context failures. The cursor advances
// once, after the last path, and only when nothing failed.
func (m *Maintenance) Apply(ctx context.Context, b Batch) (BatchReport, error) {
	replayed, err := m.Replay(ctx)
	if err != nil {
		return BatchReport{}, err
	}
	cur, _, err := m.d.Journal.Cursor(ctx, m.d.RepositoryID)
	if err != nil {
		return BatchReport{}, err
	}
	gen := cur.Generation + 1
	rep, err := m.run(ctx, normalizeRels(b.Changed), func(string) int { return gen })
	if err != nil {
		return rep, err
	}
	if len(replayed.Failed) > 0 || len(rep.Failed) > 0 {
		return rep, nil
	}
	return rep, m.d.Journal.AdvanceCursor(ctx, Cursor{
		RepositoryID: m.d.RepositoryID, FSSeq: b.FSSeq, Commit: b.Commit, TreeOID: b.TreeOID, Generation: gen,
	})
}

// Replay re-runs every unfinished intent from leg 1. When all complete, the
// cursor generation catches up to the highest replayed generation.
func (m *Maintenance) Replay(ctx context.Context) (BatchReport, error) {
	ins, err := m.d.Journal.intents(ctx, m.d.RepositoryID)
	if err != nil || len(ins) == 0 {
		return BatchReport{}, err
	}
	gens := make(map[string]int, len(ins))
	rels := make([]string, 0, len(ins))
	top := 0
	for _, in := range ins {
		gens[in.Rel] = in.Generation
		rels = append(rels, in.Rel)
		top = max(top, in.Generation)
	}
	rep, err := m.run(ctx, rels, func(rel string) int { return gens[rel] })
	if err != nil || len(rep.Failed) > 0 {
		return rep, err
	}
	cur, _, err := m.d.Journal.Cursor(ctx, m.d.RepositoryID)
	if err != nil {
		return rep, err
	}
	cur.RepositoryID = m.d.RepositoryID
	cur.Generation = max(cur.Generation, top)
	return rep, m.d.Journal.AdvanceCursor(ctx, cur)
}

// run admits rels in slices of MaxAdmitBatch and applies each admitted or
// refused path in order. gen gives the intent generation of a path.
func (m *Maintenance) run(ctx context.Context, rels []string, gen func(string) int) (BatchReport, error) {
	var rep BatchReport
	prior, err := m.d.Journal.Paths(ctx, m.d.RepositoryID)
	if err != nil {
		return rep, err
	}
	ins, err := m.d.Journal.intents(ctx, m.d.RepositoryID)
	if err != nil {
		return rep, err
	}
	open := make(map[string]intent, len(ins))
	for _, in := range ins {
		open[in.Rel] = in
	}
	for start := 0; start < len(rels); start += MaxAdmitBatch {
		slice := rels[start:min(start+MaxAdmitBatch, len(rels))]
		adms, aerr := AdmitBatch(ctx, m.d.Root, slice)
		if aerr != nil {
			return rep, aerr
		}
		for _, adm := range adms {
			if err := ctx.Err(); err != nil {
				return rep, cascade.Wrap(cascade.KindCanceled, err, "repo: maintenance canceled")
			}
			pr, had := prior[adm.Rel]
			in, hasIntent := open[adm.Rel]
			plan := pathPlan{prior: pr, had: had, intent: in, hasIntent: hasIntent, gen: gen(adm.Rel)}
			if err := m.applyOne(ctx, adm, plan, &rep); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// normalizeRels folds separators to "/", drops empties and duplicates, sorts.
func normalizeRels(changed []string) []string {
	seen := make(map[string]bool, len(changed))
	out := make([]string, 0, len(changed))
	for _, c := range changed {
		c = filepath.ToSlash(c)
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// family is one detector family and the repo-relative paths its evidence
// can change through.
type family struct {
	lang  Language
	det   Detector
	names []string
	exts  []string
}

var families = []family{
	{LanguageGo, goDetector{}, []string{"go.mod", "go.sum", "go.work"}, []string{".go"}},
	{LanguageJSTS, jstsDetector{}, []string{"package.json", "pnpm-lock.yaml", "package-lock.json", "yarn.lock", "tsconfig.json"},
		[]string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}},
	{LanguageRust, rustDetector{}, []string{"Cargo.toml", "Cargo.lock"}, []string{".rs"}},
	{LanguagePython, pythonDetector{}, []string{"pyproject.toml", "requirements.txt", "setup.py", "uv.lock"}, []string{".py"}},
	{LanguageSwift, swiftDetector{}, []string{"Package.swift"}, []string{".swift", ".xcodeproj", ".xcworkspace"}},
}

// familiesFor returns the families whose evidence rel can change (every one
// for a Makefile, whose targets override all commands).
func familiesFor(rel string) []family {
	base := path.Base(rel)
	var out []family
	for _, f := range families {
		hit := base == "Makefile" || path.Ext(base) != "" && slices.Contains(f.exts, path.Ext(base))
		for _, n := range f.names {
			hit = hit || base == n
		}
		for _, x := range f.exts { // a *.xcodeproj directory member
			hit = hit || strings.Contains(rel, x+"/")
		}
		if hit {
			out = append(out, f)
		}
	}
	return out
}

// repoLevelTouched reports which repo-level fact groups rel can change.
func repoLevelTouched(rel string, created bool) (layout, ci, harness bool) {
	base := path.Base(rel)
	ci = strings.HasPrefix(rel, ".github/workflows/") || rel == ".gitlab-ci.yml" || strings.HasPrefix(rel, ".circleci/")
	harness = base == "CLAUDE.md" || base == "AGENTS.md" || strings.HasPrefix(rel, ".claude/")
	return created, ci, harness
}

// setLanguage replaces (or removes, when undetected) lang's entry, keeping
// the registry order.
func setLanguage(langs []LanguageFacts, lang Language, facts LanguageFacts) []LanguageFacts {
	out := langs[:0:0]
	for _, l := range langs {
		if l.Language != lang {
			out = append(out, l)
		}
	}
	if facts.Detected {
		out = append(out, facts)
	}
	order := func(l Language) int {
		return slices.IndexFunc(families, func(f family) bool { return f.lang == l })
	}
	slices.SortStableFunc(out, func(a, b LanguageFacts) int { return order(a.Language) - order(b.Language) })
	return out
}

// isPathChar reports a byte that continues a path segment.
func isPathChar(c byte) bool {
	return c == '.' || c == '_' || c == '-' || c == '/' || c == '\\' || c == '~' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// citesPath reports whether s contains rel as a whole path: not glued to a
// longer path on either side (a trailing sentence period does not count).
func citesPath(s, rel string) bool {
	for off := 0; ; {
		i := strings.Index(s[off:], rel)
		if i < 0 {
			return false
		}
		start, end := off+i, off+i+len(rel)
		off = start + 1
		if start > 0 && isPathChar(s[start-1]) {
			continue
		}
		if end < len(s) && isPathChar(s[end]) && (s[end] != '.' || (end+1 < len(s) && isPathChar(s[end+1]))) {
			continue
		}
		return true
	}
}
