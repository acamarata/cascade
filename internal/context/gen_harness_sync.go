package context

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the multi-harness working-directory pipeline. Discovers the
//   tiers above a working directory once, merges them once, then renders
//   and materializes the instruction files of every harness cascade knows
//   how to write.
// Inputs: a working directory, a HomeDirFunc and a WritePolicy.
// Outputs: one WriteResult per file considered; typed cascade.Errors
//   otherwise.
// Constraints: discovery and merge run exactly once, so every harness is
//   rendered from the same MergedContext and cannot disagree about what the
//   cascade said.
// SPORT: context-engine/harness-gen (ADD, per T-3 sport_updates).

// harnessWriters is the registry of every harness serializer that ships.
//
// It is the single place a new harness is added, and everything that must
// hold across harnesses is driven from it: the pipeline below writes
// whatever is listed here, and the cross-writer conformance test asserts
// the shared behaviour over the same list. Adding an entry therefore opts a
// new writer into the conformance suite rather than leaving that to be
// remembered.
func harnessWriters() []HarnessGenerator {
	return []HarnessGenerator{
		&CCInstructionWriter{},
		&CXInstructionWriter{},
		&OCInstructionWriter{},
	}
}

// GenerateHarnessInstructions is the whole working-directory pipeline for
// every harness: discover the tiers above cwd, merge them under the
// precedence rule, render each harness's files from that one merge, and
// materialize each file into its own tier's root under policy.
//
// It returns one WriteResult per file it considered, including the ones it
// left alone. On the first write it cannot perform it returns the typed
// error and the results gathered so far, because a partial run the caller
// can see is more useful than a partial run it cannot.
//
// Two harnesses that read the same file at the same path are written once,
// not twice: the second write would be a no-op, and reporting it as a
// second result would tell a caller two files changed when one did.
//
// homeDir may be nil, in which case os.UserHomeDir is used; pass a fake in
// tests so the run never reads the developer's real home directory.
func GenerateHarnessInstructions(
	ctx context.Context, cwd string, homeDir HomeDirFunc, policy WritePolicy,
) ([]WriteResult, error) {
	records, err := Discover(ctx, cwd, homeDir)
	if err != nil {
		return nil, err
	}
	merged, err := MergeTiers(records)
	if err != nil {
		return nil, err
	}
	roots := make(map[TierRole]string, len(records))
	for _, rec := range records {
		roots[rec.Role] = rec.Dir
	}

	var results []WriteResult
	written := make(map[string]struct{}, len(records)*len(harnessWriters()))
	for _, w := range harnessWriters() {
		files, gerr := w.Generate(merged)
		if gerr != nil {
			return results, gerr
		}
		batch, werr := writeHarnessBatch(files, roots, policy, written)
		results = append(results, batch...)
		if werr != nil {
			return results, werr
		}
	}
	return results, nil
}

// writeHarnessBatch materializes one harness's files, skipping any path an
// earlier harness in the same run already wrote.
func writeHarnessBatch(
	files []HarnessFile, roots map[TierRole]string, policy WritePolicy, written map[string]struct{},
) ([]WriteResult, error) {
	results := make([]WriteResult, 0, len(files))
	for _, f := range files {
		root := roots[f.Role]
		if root == "" {
			return results, cascade.Newf(cascade.KindInternal,
				"context: generate: tier %s contributed sections but discovery gave it no directory", f.Role)
		}
		path := filepath.Join(root, filepath.FromSlash(f.Name))
		if _, seen := written[path]; seen {
			continue
		}
		written[path] = struct{}{}
		res, err := WriteHarnessFile(path, f, policy)
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}
