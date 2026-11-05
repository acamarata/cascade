package repo

// Purpose: the bounded reconciliation sweep: replay unfinished intents, then
//   re-apply only the paths whose content hash differs from the journal
//   (contract:repo-change-feed).
// Inputs: a Maintenance pipeline, its WatchJournal and an Attention func.
// Outputs: the count of repaired paths and one repo.drift attention item
//   when the count is non-zero.
// Constraints: Replay is always the first action; an unchanged tree marker
//   with a clean status walks nothing; otherwise every candidate path is
//   hashed (never parsed or indexed) and only differing paths reach Apply.
// SPORT: repo/change-feed/ADD (contract:repo-change-feed, P1-GEN-14).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ReconcileInterval is how often a watcher runs a sweep without a gap.
const ReconcileInterval = 10 * time.Minute

// openForHash opens a path for hashing; a test seam counts the reads.
var openForHash = func(name string) (io.ReadCloser, error) { return os.Open(name) } //nolint:gosec // repo-confined path

// Reconciler sweeps one repository back into agreement with its journal.
type Reconciler struct {
	m         *Maintenance
	j         *WatchJournal
	attention Attention
}

// NewReconciler validates its dependencies; any nil one is KindInvalidInput.
func NewReconciler(m *Maintenance, j *WatchJournal, a Attention) (*Reconciler, error) {
	if m == nil || j == nil || a == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: reconciler requires a maintenance pipeline, a journal and an Attention func")
	}
	return &Reconciler{m: m, j: j, attention: a}, nil
}

// Sweep replays first; a tree OID equal to the cursor's with no porcelain
// changes repairs nothing. Otherwise it hashes every tracked and changed
// path, applies only those whose hash differs from the journal (an absent
// marker makes every path differ) and raises one drift attention item when
// it repaired any.
func (r *Reconciler) Sweep(ctx context.Context) (int, error) {
	id, root := r.m.d.RepositoryID, r.m.d.Root
	if _, err := r.m.Replay(ctx); err != nil {
		return 0, err
	}
	cur, ok, err := r.j.Cursor(ctx, id)
	if err != nil {
		return 0, err
	}
	gs, err := ReadGitState(ctx, root, cur.Commit)
	if isGap(err) {
		gs, err = ReadGitState(ctx, root, "")
	}
	if err != nil {
		return 0, err
	}
	marker := ok && cur.TreeOID != ""
	if marker && cur.TreeOID == gs.TreeOID && len(gs.Changed) == 0 {
		return 0, nil
	}
	drift, err := r.drifted(ctx, gs.Changed, marker)
	if err != nil {
		return 0, err
	}
	rep, err := r.m.Apply(ctx, Batch{Changed: drift, FSSeq: cur.FSSeq, Commit: gs.Head, TreeOID: gs.TreeOID})
	if err != nil {
		return 0, err
	}
	n := len(rep.Applied) + len(rep.Refused)
	if n == 0 {
		return 0, nil
	}
	ref := fmt.Sprintf("repo.drift:%s:%s", id, gs.TreeOID)
	return n, r.attention(ctx, ref, fmt.Sprintf("%d paths repaired", n))
}

// drifted returns the candidate paths (tracked, changed and journaled) whose
// content hash differs from the journal; without a marker every one differs.
func (r *Reconciler) drifted(ctx context.Context, changed []string, marker bool) ([]string, error) {
	tracked, err := lsFiles(ctx, r.m.d.Root)
	if err != nil {
		return nil, err
	}
	prior, err := r.j.Paths(ctx, r.m.d.RepositoryID)
	if err != nil {
		return nil, err
	}
	cands := append(tracked, changed...)
	for rel := range prior {
		cands = append(cands, rel)
	}
	var out []string
	for _, rel := range normalizeRels(cands) {
		if err := canceled(ctx); err != nil {
			return nil, err
		}
		if !marker {
			out = append(out, rel)
			continue
		}
		sum, herr := hashFile(filepath.Join(r.m.d.Root, filepath.FromSlash(rel)))
		if herr != nil || sum != prior[rel].ContentSHA256 {
			out = append(out, rel) // an unreadable path goes to Apply, which reports it
		}
	}
	return out, nil
}

// hashFile is the hex SHA-256 of a regular file; a missing or non-regular
// path hashes to "" (as an unjournaled path does).
func hashFile(full string) (string, error) {
	st, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !st.Mode().IsRegular() {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	f, err := openForHash(full)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// isGap reports whether err is ErrGap itself (identity, not Kind).
func isGap(err error) bool {
	var ce *cascade.Error
	return errors.As(err, &ce) && ce == ErrGap
}

// flush runs the one Sweep a pending gap schedules, or else applies the
// pending paths once DebounceWindow has passed since the last event. An
// unreadable journal or ErrGap from git is a gap swept in place of the batch.
// Nothing starts while the watcher is quiesced.
func (w *Watcher) flush(ctx context.Context) error {
	if !w.begin() {
		return nil
	}
	defer w.end()
	if w.gap {
		w.gap, w.pending = false, map[string]bool{}
		_, err := w.sweep(ctx)
		return err
	}
	if len(w.pending) == 0 || w.clock.Now().Sub(w.last) < DebounceWindow {
		return nil
	}
	b, err := w.batch(ctx)
	w.pending = map[string]bool{}
	if err != nil || b == nil {
		return err
	}
	return w.apply(ctx, *b)
}

// batch builds the pending Batch, or sweeps (returning nil) on a gap.
func (w *Watcher) batch(ctx context.Context) (*Batch, error) {
	cur, _, err := w.m.d.Journal.Cursor(ctx, w.m.d.RepositoryID)
	if err == nil {
		gs, gerr := ReadGitState(ctx, w.root, cur.Commit)
		if gerr == nil {
			return &Batch{Changed: w.pendingRels(), FSSeq: cur.FSSeq + 1, Commit: gs.Head, TreeOID: gs.TreeOID}, nil
		}
		if !isGap(gerr) {
			return nil, gerr
		}
	}
	_, err = w.sweep(ctx)
	return nil, err
}

// begin marks a batch in flight unless the watcher is quiesced.
func (w *Watcher) begin() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.quiesced {
		return false
	}
	w.inflight = make(chan struct{})
	return true
}

// end closes the in-flight mark, releasing any Quiesce waiting on it.
func (w *Watcher) end() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.inflight)
	w.inflight = nil
}

// Quiesce stops new batches, waits for the in-flight batch to commit and
// returns the journaled cursor.
func (w *Watcher) Quiesce(ctx context.Context) (Cursor, error) {
	w.mu.Lock()
	w.quiesced = true
	done := w.inflight
	w.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return Cursor{}, cascade.Wrap(cascade.KindCanceled, ctx.Err(), "repo: quiesce canceled")
		}
	}
	cur, _, err := w.m.d.Journal.Cursor(ctx, w.m.d.RepositoryID)
	return cur, err
}

// Resume restarts batch delivery after Quiesce; pending events are kept.
func (w *Watcher) Resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.quiesced = false
}
