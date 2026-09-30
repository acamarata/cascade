package plugins

import (
	"context"
	"fmt"
	"path/filepath"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/plugins/codex"
)

// Purpose (this file): the cascade-codex composition root. plugins/** may
//   not import internal/** (Art.10.2), so the Codex instruction writer is
//   injected from here — the one package free to import both sides —
//   moving this plugin's wiring out of registry.go per DEBT-ARCH-11.
// Inputs: internal/context's CXInstructionWriter.
// Outputs: cascade-codex's Generate seam, wired to its real implementation.
// Constraints: init() panics on a wiring error (startup programming error).
// SPORT: internal/plugins cascade-codex wiring (MOVE) — P1-CORE-02.

func init() {
	if err := codex.SetGenerator(harnessGenerator(&casctx.CXInstructionWriter{})); err != nil {
		panic("internal/plugins: wire cascade-codex generator: " + err.Error())
	}
}

// generatedFile is the internal/context-side shape shared by both harness
// adapters below, before it is translated into the target plugin's own
// GeneratedFile type.
type generatedFile struct {
	path    string
	content []byte
}

// resolveHarnessFiles runs the full internal/context pipeline once for cwd
// (discover the five tiers, merge them, render w's files) and resolves
// each rendered file's tier-relative name against its tier's own root
// directory, matching internal/context's own gen_harness_sync.go
// writeHarnessBatch resolution exactly.
func resolveHarnessFiles(ctx context.Context, cwd string, w casctx.HarnessGenerator) ([]generatedFile, error) {
	records, err := casctx.Discover(ctx, cwd, nil)
	if err != nil {
		return nil, err
	}
	merged, err := casctx.MergeTiers(records)
	if err != nil {
		return nil, err
	}
	roots := make(map[casctx.TierRole]string, len(records))
	for _, rec := range records {
		roots[rec.Role] = rec.Dir
	}
	files, err := w.Generate(merged)
	if err != nil {
		return nil, err
	}
	out := make([]generatedFile, 0, len(files))
	for _, f := range files {
		root := roots[f.Role]
		if root == "" {
			return out, fmt.Errorf("internal/plugins: tier %d contributed sections but discovery gave it no directory", f.Role)
		}
		out = append(out, generatedFile{
			path:    filepath.Join(root, filepath.FromSlash(f.Name)),
			content: f.Content,
		})
	}
	return out, nil
}

// harnessGenerator adapts w into cascade-codex's GeneratorFunc shape.
func harnessGenerator(w casctx.HarnessGenerator) codex.GeneratorFunc {
	return func(ctx context.Context, cwd string) ([]codex.GeneratedFile, error) {
		files, err := resolveHarnessFiles(ctx, cwd, w)
		if err != nil {
			return nil, err
		}
		out := make([]codex.GeneratedFile, 0, len(files))
		for _, f := range files {
			out = append(out, codex.GeneratedFile{Path: f.path, Content: f.content})
		}
		return out, nil
	}
}
