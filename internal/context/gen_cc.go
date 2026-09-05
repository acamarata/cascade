package context

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the CC harness serializer. Renders a MergedContext into
//   the per-tier CLAUDE.md content the CC harness reads, and drives the
//   whole working-directory pipeline (discover, merge, render, materialize).
// Inputs: a MergedContext for Generate; a working directory, a HomeDirFunc
//   and a WritePolicy for the pipeline entry point.
// Outputs: []HarnessFile from Generate; []WriteResult from the pipeline;
//   typed cascade.Errors otherwise.
// Constraints: Generate is pure and byte-stable. Output format is v1's
//   harvested CC header (cascade-cli generate_instructions/cc.rs at
//   archive/p9-integration) plus the one CLI-fallback line R-16.43 ratified.
// SPORT: context-engine/cc-instruction-gen (ADD, per T-3 sport_updates).

// ccTargetName is where the CC harness reads a tier's instruction
// file, relative to that tier's root directory. It is always slash-separated
// so a HarnessFile is comparable across platforms; the write path converts
// it before touching the filesystem.
const ccTargetName = ".claude/CLAUDE.md"

// CCInstructionWriter renders a merged instruction cascade into CC harness
// CLAUDE.md files, one per tier that contributed to the merge.
//
// # Why one file per tier and not one merged file
//
// The harness reads a CLAUDE.md at each level of the directory tree it is
// working in, so the cascade's shape has to survive as files on disk, not
// only as a resolved blob. A section a lower tier lost to a higher one is
// simply absent from the lower tier's file, which is what the merge decided;
// what is never absent is a whole tier, and what is never silent is a
// section this writer refused to render (see renderTierBlock).
//
// The zero value is ready to use.
type CCInstructionWriter struct{}

// Compile-time proof that the CC writer satisfies the shared seam. If this
// stops compiling, a generator and its interface have drifted apart, which
// is exactly the drift the seam exists to prevent.
var _ HarnessGenerator = (*CCInstructionWriter)(nil)

// Generate renders mc into one HarnessFile per contributing tier, ordered
// most-general-first (GCI before PAI), matching the merge's own emission
// order.
//
// A MergedContext with no sections renders no files and returns a nil error:
// a working directory with no instructions above it is a legitimate state,
// not a failure. A MergedContext that cannot have come from MergeTiers is
// refused whole with a typed KindInvalidInput error, since rendering the
// coherent half of it would ship a file missing the other half with nothing
// on the page to say so.
func (w *CCInstructionWriter) Generate(mc MergedContext) ([]HarnessFile, error) {
	if err := validateMergedContext(mc); err != nil {
		return nil, err
	}
	roles, buckets := groupByRole(mc)
	files := make([]HarnessFile, 0, len(roles))
	for _, role := range roles {
		files = append(files, HarnessFile{
			Name:    ccTargetName,
			Content: []byte(renderTierBlock(role, buckets[role]) + "\n"),
			Role:    role,
		})
	}
	return files, nil
}

// GenerateCCInstructions is the whole working-directory pipeline: discover
// the tiers above cwd, merge them under the precedence rule, render the CC
// files, and materialize each one into its own tier's root under policy.
//
// It returns one WriteResult per file it considered, including the ones it
// left alone, so a caller can report what changed without diffing anything
// itself. On the first write it cannot perform it returns the typed error
// and the results gathered so far, because a partial run the caller can see
// is more useful than a partial run it cannot.
//
// homeDir may be nil, in which case os.UserHomeDir is used; pass a fake in
// tests so the run never reads the developer's real home directory.
func GenerateCCInstructions(
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
	var w CCInstructionWriter
	files, err := w.Generate(merged)
	if err != nil {
		return nil, err
	}

	roots := make(map[TierRole]string, len(records))
	for _, rec := range records {
		roots[rec.Role] = rec.Dir
	}

	results := make([]WriteResult, 0, len(files))
	for _, f := range files {
		root := roots[f.Role]
		if root == "" {
			return results, cascade.Newf(cascade.KindInternal,
				"context: generate: tier %s contributed sections but discovery gave it no directory", f.Role)
		}
		res, werr := WriteHarnessFile(filepath.Join(root, filepath.FromSlash(f.Name)), f, policy)
		if werr != nil {
			return results, werr
		}
		results = append(results, res)
	}
	return results, nil
}
