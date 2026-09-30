package plugins

import (
	"context"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/plugins/opencode"
)

// Purpose (this file): the cascade-opencode composition root. plugins/** may
//   not import internal/** (Art.10.2), so the OpenCode instruction writer is
//   injected from here — the one package free to import both sides —
//   moving this plugin's wiring out of registry.go per DEBT-ARCH-11.
// Inputs: internal/context's OCInstructionWriter.
// Outputs: cascade-opencode's Generate seam, wired to its real implementation.
// Constraints: init() panics on a wiring error (startup programming error).
// SPORT: internal/plugins cascade-opencode wiring (MOVE) — P1-CORE-02.

func init() {
	if err := opencode.SetGenerator(harnessGeneratorOC(&casctx.OCInstructionWriter{})); err != nil {
		panic("internal/plugins: wire cascade-opencode generator: " + err.Error())
	}
}

// harnessGeneratorOC adapts w into cascade-opencode's GeneratorFunc shape.
// A second function, rather than a generic helper, because the two
// plugins' GeneratedFile types are deliberately distinct (each plugin owns
// its own internal/-free vocabulary; see plugins/codex/install.go's
// GENERATOR SEAM note).
func harnessGeneratorOC(w casctx.HarnessGenerator) opencode.GeneratorFunc {
	return func(ctx context.Context, cwd string) ([]opencode.GeneratedFile, error) {
		files, err := resolveHarnessFiles(ctx, cwd, w)
		if err != nil {
			return nil, err
		}
		out := make([]opencode.GeneratedFile, 0, len(files))
		for _, f := range files {
			out = append(out, opencode.GeneratedFile{Path: f.path, Content: f.content})
		}
		return out, nil
	}
}
