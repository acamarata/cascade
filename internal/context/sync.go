package context

// Purpose: drift detection and regeneration for the harness instruction
//   files GenerateHarnessInstructions (S-09.T3) writes. DriftCheck answers
//   "does the on-disk file still match what the generators would produce
//   right now", without writing anything; Sync answers the same question
//   and, unless checkOnly is set, fixes any answer that is "no".
// Inputs: a MergedContext plus the tier-root map Discover's own
//   []TierRecord supplies (DriftCheck); a working directory and a
//   HomeDirFunc (Sync, which runs discovery itself so a caller never has to
//   assemble the map by hand).
// Outputs: []DriftResult / SyncResult; a typed cascade.Error on the first
//   generator failure, never a panic.
// Constraints: 04-PEWS-PLAN-W1-W3.md Wave 2 Epic E S-09 T4. No bare
//   time.Now (this file has no time dependency at all — see the journal's
//   contract/tree note on why "generation" here needs no clock). Read-only
//   filesystem access in DriftCheck; Sync's regenerate path delegates every
//   write to WriteHarnessFile's own atomic temp-file + os.Rename, so a
//   crash mid-sync leaves each individual file either fully old or fully
//   new, never half of either — see this ticket's journal for what a crash
//   BETWEEN files leaves (the files already swapped keep their new
//   content; the files not yet reached keep their old content; nothing is
//   torn, and the very next `cascade context sync` finishes the rest,
//   because every unwritten file is still reported stale).
// SPORT: context-engine/sync (ADD, per T-4 sport_updates).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// DriftResult describes one harness instruction file's freshness relative
// to a generation run from the current MergedContext right now.
//
// A missing on-disk file is Stale, not an error: this repository has three
// separate ways to observe "no instructions have ever been written here"
// (a fresh clone, a fresh install, a tier the last sync run never reached)
// and exactly one of them is actually a problem. Collapsing them into one
// boolean would make a fresh checkout report identically to a checkout that
// silently stopped syncing years ago; DriftResult keeps that visible through
// Reason ("missing on disk" reads differently from "content differs from a
// fresh generation" in a --check table) rather than through a bit no reader
// has to know to ask about (see this ticket's journal for the full "what
// does drift mean here" note).
type DriftResult struct {
	// Harness is the short, product-neutral generator id: "claude",
	// "codex" or "opencode".
	Harness string
	// Path is the on-disk file considered. Empty when Reason describes a
	// whole-harness failure (Stale is still true in that case).
	Path string
	// Stale reports whether Path's content, or its absence, differs from
	// what the corresponding HarnessGenerator would produce right now.
	Stale bool
	// Reason names why Stale is true. Empty when Stale is false.
	Reason string
}

// SyncResult summarizes one DriftCheck or one regeneration run.
type SyncResult struct {
	// Drift is populated by a check-only run: one entry per file the
	// generators would produce, in generator order.
	Drift []DriftResult
	// Files is populated by a regenerate run: WriteHarnessFile's own
	// per-file outcome, including files left ActionUnchanged.
	Files []WriteResult
	// Regenerated counts Files entries whose Action was not
	// ActionUnchanged — the number a caller should read as "this run
	// wrote something".
	Regenerated int
	// AlreadyFresh counts Files entries whose Action was ActionUnchanged.
	AlreadyFresh int
}

// harnessName returns w's short, product-neutral identifier, matching the
// three names this ticket's contract fixes ("claude, codex, opencode").
// harnessWriters() is the only place a fourth writer would be added, and
// this switch is the only other place that would need to grow with it — a
// missing case falls back to "unknown" rather than panicking, so an
// unregistered generator still produces a readable (if unhelpfully named)
// row instead of crashing a drift check.
func harnessName(w HarnessGenerator) string {
	switch w.(type) {
	case *CCInstructionWriter:
		return "claude"
	case *CXInstructionWriter:
		return "codex"
	case *OCInstructionWriter:
		return "opencode"
	default:
		return "unknown"
	}
}

// DriftCheck compares every harness's on-disk instruction file against a
// fresh Generate(mc) run, for every tier a file in roots. roots maps a
// contributing TierRole to the directory its file lives under — the same
// map GenerateHarnessInstructions builds from Discover's []TierRecord
// (rec.Dir keyed by rec.Role); Sync builds it that way below so a caller
// driving DriftCheck directly from its own Discover run can never disagree
// with Sync about where a file lives.
//
// It never panics: mc's own generators already treat a zero-value or
// empty-Sections MergedContext as "nothing to render" (HarnessGenerator's
// own contract), so this function's loops simply run zero times. The one
// error a generator DOES return — a MergedContext that could not have come
// from MergeTiers — is a typed cascade.Error already (gen_cc_errors.go);
// DriftCheck neither invents a new error nor discards that one: the failing
// harness is recorded Stale with the error's own message as Reason, and the
// SAME typed error is returned alongside the partial results gathered so
// far, so a caller sees both "which harness could not even be checked" and
// a fail-closed non-nil error rather than a clean-looking check that never
// actually ran for the rest of the harnesses.
func DriftCheck(ctx context.Context, mc MergedContext, roots map[TierRole]string) ([]DriftResult, error) {
	_ = ctx // no I/O in this package's generators needs cancellation today
	var results []DriftResult
	seen := make(map[string]struct{}, len(roots)*3)
	for _, w := range harnessWriters() {
		name := harnessName(w)
		files, err := w.Generate(mc)
		if err != nil {
			results = append(results, DriftResult{Harness: name, Stale: true, Reason: err.Error()})
			return results, err
		}
		results = append(results, driftForFiles(name, files, roots, seen)...)
	}
	return results, nil
}

// driftForFiles reports drift for one harness's freshly generated files,
// skipping any path an earlier harness in the same run already reported —
// two harnesses that read the same file at the same path (the AGENTS.md
// pair) must not be reported twice under two different harness names for
// what is, on disk, one file.
func driftForFiles(name string, files []HarnessFile, roots map[TierRole]string, seen map[string]struct{}) []DriftResult {
	results := make([]DriftResult, 0, len(files))
	for _, f := range files {
		root := roots[f.Role]
		if root == "" {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(f.Name))
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		stale, reason := fileDrift(path, f)
		results = append(results, DriftResult{Harness: name, Path: path, Stale: stale, Reason: reason})
	}
	return results
}

// fileDrift reports whether path's on-disk content differs from f, reusing
// mergeManagedBlock — the exact splice WriteHarnessFile itself would
// perform — so drift-check and regeneration can never disagree about what
// "stale" means. It only reads the filesystem; it never writes.
func fileDrift(path string, f HarnessFile) (stale bool, reason string) {
	existing, err := os.ReadFile(path) //nolint:gosec // path is composed from discovered tier roots.
	switch {
	case os.IsNotExist(err):
		return true, "missing on disk"
	case err != nil:
		return true, "read error: " + err.Error()
	}
	block := strings.TrimRight(string(f.Content), "\n")
	next, _, mErr := mergeManagedBlock(path, string(existing), block, RefuseIfEdited)
	if mErr != nil {
		// RefuseIfEdited's only error is the hand-edited-block conflict
		// (handEditRefusal): the file has diverged from what cascade
		// would write regardless of how, so it is reported stale for
		// that reason rather than surfaced as a fatal error a --check
		// run never asked to fail on.
		return true, "hand-edited managed block differs from a fresh generation"
	}
	if next == string(existing) {
		return false, ""
	}
	return true, "content differs from a fresh generation"
}

// Sync runs the S-08 discover+merge pipeline once for cwd, then either
// checks (checkOnly) or regenerates every harness's instruction files
// against the result.
//
// The regenerate path delegates to GenerateHarnessInstructions —
// WriteHarnessFile's own atomic write and its "identical bytes are skipped
// entirely" rule already make a second run over an already-synced tree a
// no-op (harness_gen.go's own doc comment) — rather than restating that
// contract here against a second, independently-computed notion of
// staleness that could in principle disagree with it.
func Sync(ctx context.Context, cwd string, homeDir HomeDirFunc, checkOnly bool) (SyncResult, error) {
	if checkOnly {
		records, err := Discover(ctx, cwd, homeDir)
		if err != nil {
			return SyncResult{}, err
		}
		merged, err := MergeTiers(records)
		if err != nil {
			return SyncResult{}, err
		}
		drift, err := DriftCheck(ctx, merged, rootsByRole(records))
		return SyncResult{Drift: drift}, err
	}
	results, err := GenerateHarnessInstructions(ctx, cwd, homeDir, RefuseIfEdited)
	sr := SyncResult{Files: results}
	for _, r := range results {
		if r.Action == ActionUnchanged {
			sr.AlreadyFresh++
		} else {
			sr.Regenerated++
		}
	}
	return sr, err
}

// rootsByRole builds the TierRole -> directory map DriftCheck needs from a
// []TierRecord, matching GenerateHarnessInstructions' own internal
// construction of the same map (gen_harness_sync.go) so the two can never
// resolve a file to two different directories.
func rootsByRole(records []TierRecord) map[TierRole]string {
	roots := make(map[TierRole]string, len(records))
	for _, rec := range records {
		roots[rec.Role] = rec.Dir
	}
	return roots
}
