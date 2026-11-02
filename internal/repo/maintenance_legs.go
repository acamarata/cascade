package repo

// Purpose: the per-path legs of the maintenance pipeline and their journal
//   bookkeeping: admission verdict, detector family, scoped Go graph, FTS,
//   vectors, stale-fact rejection (contract:repo-maintenance).
// Inputs: one admitted or refused path plus its journaled prior state.
// Outputs: effects in the stores and the IndexLeg; one report line per path.
// Constraints: legs run in fixed order and each is idempotent for the same
//   (path, content hash); the path row and intent clear in one transaction
//   after the last leg; a leg error leaves the intent for Replay.
// SPORT: repo/maintenance-pipeline/ADD (P1-GEN-04).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/acamarata/cascade/pkg/cascade"
)

// changeReason is the rejection reason stale facts receive.
const changeReason = "source path changed - pending re-validation"

// legAttention marks the refusal's Attention call as made (legs 1-6 use bits 0-5).
const legAttention = 1 << 6

// onDetect is a test seam: it sees every detector family a leg reruns.
var onDetect = func(Language) {}

// pathPlan is a path's journaled prior state.
type pathPlan struct {
	prior     PathState
	had       bool
	intent    intent
	hasIntent bool
	gen       int
}

// pathRun is one path moving through the legs.
type pathRun struct {
	pathPlan
	rel         string
	content     []byte
	hash        string
	deleted     bool
	adm         Admission
	chunkIDs    []string
	pending     bool
	invalidated []string
}

type leg struct {
	n  int
	fn func(context.Context, *pathRun) error
}

func (m *Maintenance) legsFor(p *pathRun) []leg {
	if !p.adm.Admitted && !p.deleted {
		return []leg{{1, m.legRefuse}}
	}
	return []leg{
		{1, func(context.Context, *pathRun) error { return nil }},
		{2, m.legDetect}, {3, m.legGraph}, {4, m.legFTS}, {5, m.legVector}, {6, m.legLedger},
	}
}

// applyOne drives one path through intent, legs and the closing transaction.
// Its error is only for journal or context failures.
func (m *Maintenance) applyOne(ctx context.Context, adm Admission, plan pathPlan, rep *BatchReport) error {
	p := &pathRun{pathPlan: plan, rel: adm.Rel, adm: adm}
	if adm.Admitted {
		p.content = adm.Content
		sum := sha256.Sum256(adm.Content)
		p.hash = hex.EncodeToString(sum[:])
		if plan.had && plan.prior.ContentSHA256 == p.hash && !plan.hasIntent {
			rep.Unchanged = append(rep.Unchanged, p.rel)
			return nil
		}
	} else {
		p.deleted = adm.Reason == ReasonUnreadable && m.missing(adm.Rel)
	}
	in := intent{Rel: p.rel, ContentSHA256: p.hash, Generation: plan.gen, Deleted: p.deleted}
	if plan.hasIntent && plan.intent.ContentSHA256 == p.hash {
		in.LegsDone = plan.intent.LegsDone
	}
	id := m.d.RepositoryID
	if err := m.d.Journal.beginIntent(ctx, id, in); err != nil {
		return err
	}
	for _, l := range m.legsFor(p) {
		if err := l.fn(ctx, p); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return cascade.Wrap(cascade.KindCanceled, cerr, "repo: maintenance canceled")
			}
			rep.Failed = append(rep.Failed, p.rel)
			return nil
		}
		if err := m.d.Journal.markLegs(ctx, id, p.rel, 1<<(l.n-1)); err != nil {
			return err
		}
		afterLeg(p.rel, l.n)
	}
	return m.finish(ctx, p, rep)
}

// finish writes the path state (or clears it) together with the intent.
func (m *Maintenance) finish(ctx context.Context, p *pathRun, rep *BatchReport) error {
	var st *PathState
	if p.adm.Admitted {
		st = &PathState{Rel: p.rel, ContentSHA256: p.hash, ChunkIDs: p.chunkIDs, VectorPending: p.pending}
	}
	if err := m.d.Journal.complete(ctx, m.d.RepositoryID, p.rel, st); err != nil {
		return err
	}
	if p.adm.Admitted || p.deleted {
		rep.Applied = append(rep.Applied, p.rel)
	} else {
		rep.Refused = append(rep.Refused, p.rel)
	}
	if p.pending {
		rep.VectorPending = append(rep.VectorPending, p.rel)
	}
	rep.Invalidated = append(rep.Invalidated, p.invalidated...)
	return nil
}

// missing reports whether rel no longer exists under the root.
func (m *Maintenance) missing(rel string) bool {
	_, err := os.Lstat(filepath.Join(m.d.Root, filepath.FromSlash(rel)))
	return os.IsNotExist(err)
}

// legRefuse deindexes a path the guard refused: old chunks go, citing facts
// are rejected, and exactly one Attention call names the path.
func (m *Maintenance) legRefuse(ctx context.Context, p *pathRun) error {
	if err := m.dropOldChunks(ctx, p); err != nil {
		return err
	}
	if err := m.rejectCiting(ctx, p, "source path refused by the ingest guard ("+p.adm.Reason+") - pending re-validation"); err != nil {
		return err
	}
	if p.intent.LegsDone&legAttention != 0 && p.hasIntent {
		return nil
	}
	text := "repo ingest refused " + p.rel + ": " + p.adm.Reason
	if p.adm.PatternClass != "" {
		text += " (" + p.adm.PatternClass + ")"
	}
	if err := m.d.Attention(ctx, "repo.ingest:"+m.d.RepositoryID+":"+p.rel, text); err != nil {
		return err
	}
	return m.d.Journal.markLegs(ctx, m.d.RepositoryID, p.rel, legAttention)
}

func (m *Maintenance) dropOldChunks(ctx context.Context, p *pathRun) error {
	if len(p.prior.ChunkIDs) == 0 {
		return nil
	}
	return m.d.Index.DeleteChunks(ctx, m.d.RepositoryID, p.prior.ChunkIDs)
}

// legFTS replaces the path's chunks: old ids out, fresh chunks in.
func (m *Maintenance) legFTS(ctx context.Context, p *pathRun) error {
	if err := m.dropOldChunks(ctx, p); err != nil {
		return err
	}
	if p.deleted {
		return nil
	}
	ids, err := m.d.Index.WriteFTS(ctx, m.d.RepositoryID, p.rel, p.content)
	p.chunkIDs = ids
	return err
}

// legVector embeds the path's chunks. A pending or failed embed is recorded
// as vector_pending and never fails the path.
func (m *Maintenance) legVector(ctx context.Context, p *pathRun) error {
	if p.deleted {
		return nil
	}
	pending, err := m.d.Index.WriteVector(ctx, m.d.RepositoryID, p.rel, p.chunkIDs, p.content)
	p.pending = pending || err != nil
	return nil
}

// legLedger rejects every live ledger row citing the changed path.
func (m *Maintenance) legLedger(ctx context.Context, p *pathRun) error {
	return m.rejectCiting(ctx, p, changeReason)
}

// rejectCiting moves proposed and accepted rows that cite p.rel to rejected
// through UpdateState; no new state exists. Rows citing other paths are untouched.
func (m *Maintenance) rejectCiting(ctx context.Context, p *pathRun, reason string) error {
	rows, err := m.d.Ledger.List(ctx, LedgerFilter{RepositoryID: m.d.RepositoryID})
	if err != nil {
		return err
	}
	for _, f := range rows {
		if (f.State != FactProposed && f.State != FactAccepted) || (!citesPath(f.Fact, p.rel) && !citesPath(f.Source, p.rel)) {
			continue
		}
		f.State, f.RejectReason = FactRejected, reason
		if err := m.d.Ledger.UpdateState(ctx, f); err != nil {
			return err
		}
		p.invalidated = append(p.invalidated, f.ID)
	}
	return nil
}

// legDetect reruns only the detector families (and repo-level scans) the
// changed path can affect, and upserts the inventory when a fact moved.
func (m *Maintenance) legDetect(ctx context.Context, p *pathRun) error {
	fams := familiesFor(p.rel)
	layout, ci, harness := repoLevelTouched(p.rel, !p.had || p.deleted)
	if len(fams) == 0 && !layout && !ci && !harness {
		return nil
	}
	inv, ok, err := m.d.Inventory.Get(ctx, m.d.RepositoryID)
	if err != nil || !ok {
		return err
	}
	next := inv
	next.Languages = slices.Clone(inv.Languages)
	over, err := makefileOverrides(m.d.Root)
	if err != nil {
		return err
	}
	for _, f := range fams {
		onDetect(f.lang)
		facts, derr := f.det.Detect(ctx, m.d.Root)
		if derr != nil {
			return derr
		}
		next.Languages = setLanguage(next.Languages, f.lang, applyOverrides(facts, over))
	}
	if next.Languages, err = m.settleGeneric(ctx, next.Languages, over); err != nil {
		return err
	}
	if err := m.rescanRepoLevel(&next, layout, ci, harness); err != nil {
		return err
	}
	if reflect.DeepEqual(inv, next) {
		return nil
	}
	next.ScannedAt = m.d.Clock.Now().Unix()
	return m.d.Inventory.Upsert(ctx, next)
}

// settleGeneric keeps the generic fallback exactly when no language family
// is detected, as DetectAll does.
func (m *Maintenance) settleGeneric(ctx context.Context, langs []LanguageFacts, over map[string]bool) ([]LanguageFacts, error) {
	var detected []LanguageFacts
	for _, l := range langs {
		if l.Language != LanguageGeneric {
			detected = append(detected, l)
		}
	}
	if len(detected) > 0 {
		return detected, nil
	}
	facts, err := (genericDetector{}).Detect(ctx, m.d.Root)
	if err != nil {
		return nil, err
	}
	return []LanguageFacts{applyOverrides(facts, over)}, nil
}

// rescanRepoLevel refreshes the layout, CI and harness facts that rel touched.
func (m *Maintenance) rescanRepoLevel(inv *Inventory, layout, ci, harness bool) error {
	var err error
	if layout {
		if inv.Layout, err = ScanLayout(m.d.Root); err != nil {
			return err
		}
	}
	if ci {
		if inv.CI, err = ScanCI(m.d.Root); err != nil {
			return err
		}
	}
	if harness {
		inv.Harness, err = ScanHarness(m.d.Root)
	}
	return err
}
