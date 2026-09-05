package context

// Purpose: the AGENTS.md harness serializers. Renders a MergedContext into
//   the instruction files two further coding harnesses read, reusing the CC
//   writer's managed-block renderer rather than restating it, so all three
//   writers hold back the same content, carry the same digest guard and
//   emit tiers in the same order by construction.
// Inputs: a MergedContext, as produced by MergeTiers.
// Outputs: []HarnessFile, or a typed cascade.Error of KindInvalidInput.
// Constraints: pure, deterministic and byte-stable. No clock, no
//   filesystem, no map iteration on the output path. Target file names come
//   from captured invocations of the real tools; see
//   testdata/goldens/README.md.
// SPORT: context-engine/harness-gen (ADD, per T-3 sport_updates).

// # Why these two targets share one renderer
//
// The ticket assumed two further instruction FORMATS. Probing the two real
// tools says otherwise: both read a file called AGENTS.md, both take its
// bytes verbatim as Markdown, and neither imposes any schema on it. The
// captured evidence is in testdata/goldens/ (a real prompt-input dump from
// one tool, the shipped instruction-path resolver from the other).
//
// So the honest shape is one renderer with a per-target descriptor, not two
// serializers that would agree on the day they were written and drift the
// first time either was touched. What actually differs between the targets
// is WHERE each one looks for the global tier's file, which is why the
// descriptor exists at all.

// agentsTarget describes one AGENTS.md-consuming harness.
//
// tierName is the file every tier below GCI is read from, relative to that
// tier's root. globalName is the GCI tier's file, relative to the user's
// home directory: neither tool reads a bare ~/AGENTS.md, so emitting one
// there would produce a file the harness silently ignores.
type agentsTarget struct {
	// id is the target's short, product-neutral identifier, matching the
	// abbreviation style the CC writer established.
	id string
	// tierName is the per-tier file name, in slash form.
	tierName string
	// globalName is the GCI tier's file name, relative to the home
	// directory, in slash form.
	globalName string
}

// agentsFileName is the standard per-tier file name both targets read. The
// name is fixed by the harnesses, not by cascade, and is the same string
// for both of them.
const agentsFileName = "AGENTS.md"

// cxTarget is the first AGENTS.md harness. Its global instruction file
// lives under the tool's own home directory, which defaults to ~/.codex
// (captured in testdata/goldens/cx/codex-home.capture.txt); the ingestion
// capture next to it shows a file placed there arriving as the
// most-general block of the prompt.
var cxTarget = agentsTarget{
	id:         "cx",
	tierName:   agentsFileName,
	globalName: ".codex/" + agentsFileName,
}

// ocTarget is the second AGENTS.md harness. Its global instruction file is
// AGENTS.md under its XDG config directory, which defaults to
// ~/.config/opencode (captured in
// testdata/goldens/oc/debug-paths.capture.txt).
//
// An XDG_CONFIG_HOME override moves that directory. Resolving environment
// overrides is the write path's job, not a pure generator's; the default
// location is what this name records, and the override is named in
// testdata/goldens/README.md so the write path inherits a known gap rather
// than discovering one.
var ocTarget = agentsTarget{
	id:         "oc",
	tierName:   agentsFileName,
	globalName: ".config/opencode/" + agentsFileName,
}

// fileName returns target's file for role, relative to that tier's root.
func (t agentsTarget) fileName(role TierRole) string {
	if role == TierGCI {
		return t.globalName
	}
	return t.tierName
}

// CXInstructionWriter renders a merged instruction cascade into the first
// AGENTS.md harness's files, one per tier that contributed to the merge.
//
// The zero value is ready to use.
type CXInstructionWriter struct{}

// Compile-time proof that this writer satisfies the shared seam. If it
// stops compiling, a generator and its interface have drifted apart, which
// is exactly the drift the seam exists to prevent.
var _ HarnessGenerator = (*CXInstructionWriter)(nil)

// Generate renders mc for the first AGENTS.md target. Its ordering,
// validation, exclusion and digest behaviour are the CC writer's, called
// rather than copied.
func (w *CXInstructionWriter) Generate(mc MergedContext) ([]HarnessFile, error) {
	return generateAgentsFiles(cxTarget, mc)
}

// OCInstructionWriter renders a merged instruction cascade into the second
// AGENTS.md harness's files, one per tier that contributed to the merge.
//
// The zero value is ready to use.
type OCInstructionWriter struct{}

// Compile-time proof that this writer satisfies the shared seam.
var _ HarnessGenerator = (*OCInstructionWriter)(nil)

// Generate renders mc for the second AGENTS.md target.
func (w *OCInstructionWriter) Generate(mc MergedContext) ([]HarnessFile, error) {
	return generateAgentsFiles(ocTarget, mc)
}

// generateAgentsFiles is the whole body of both writers.
//
// It validates through validateMergedContext, orders through groupByRole
// and renders through renderTierBlock: the same three functions the CC
// writer calls, so a change to any of them moves all three writers at once.
// An empty MergedContext renders no files and no error, matching the CC
// writer: a working directory with no instructions above it is a legitimate
// state, not a failure.
func generateAgentsFiles(target agentsTarget, mc MergedContext) ([]HarnessFile, error) {
	if err := validateMergedContext(mc); err != nil {
		return nil, err
	}
	roles, buckets := groupByRole(mc)
	files := make([]HarnessFile, 0, len(roles))
	for _, role := range roles {
		files = append(files, HarnessFile{
			Name:    target.fileName(role),
			Content: []byte(renderTierBlock(role, buckets[role]) + "\n"),
			Role:    role,
		})
	}
	return files, nil
}
