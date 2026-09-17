package plugins

// Purpose: compute which instruction files an installed harness still
//   reads, so uninstalling one of two harnesses that share a file does not
//   de-configure the other (P1-E16-W4-S35-T8, R-14.265).
// Inputs: the real harness detector and the real per-harness generators.
// Outputs: a per-plugin SharedPaths set, keyed by the path a harness OTHER
//   than the one being removed would generate.
// Constraints: this is the only package that can compute it. plugins/**
//   may import pkg/** and never internal/** (Art.10.2), so no adapter can
//   ask the detector what is installed — and a base-name heuristic on that
//   side would refuse to remove AGENTS.md on a machine where only one
//   harness was ever installed. The composition root already bridges both
//   sides; the decision is made here and passed in.
// SPORT: internal/plugins shared instruction paths (ADD) —
//   P1-E16-W4-S35-T8.

import (
	"context"
	"os"
	goruntime "runtime"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/plugins/claude"
	"github.com/acamarata/cascade/plugins/codex"
	"github.com/acamarata/cascade/plugins/opencode"
)

// harnessGeneratorsByKind names each shipped harness's generator, so the
// shared set is computed from what each install ACTUALLY writes rather
// than from a list of file names kept here.
//
// Art.1: a hard-coded "AGENTS.md is shared by codex and opencode" would
// pass every test in this ticket and be a stub. A harness that changes its
// instruction file name, or a fourth harness that starts sharing one,
// changes this answer with no edit here.
func harnessGeneratorsByKind() map[casctx.HarnessKind]func(context.Context, string) ([]string, error) {
	return map[casctx.HarnessKind]func(context.Context, string) ([]string, error){
		casctx.HarnessClaude:   pathsFromClaude,
		casctx.HarnessCodex:    pathsFromCodex,
		casctx.HarnessOpenCode: pathsFromOpenCode,
	}
}

func pathsFromClaude(ctx context.Context, cwd string) ([]string, error) {
	files, err := claude.Generate(ctx, cwd)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out, nil
}

func pathsFromCodex(ctx context.Context, cwd string) ([]string, error) {
	files, err := codex.Generate(ctx, cwd)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out, nil
}

func pathsFromOpenCode(ctx context.Context, cwd string) ([]string, error) {
	files, err := opencode.Generate(ctx, cwd)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out, nil
}

// sharedPathsFor returns the paths some INSTALLED harness other than
// removing would generate for cwd, mapped to that harness's name.
//
// Installed, not merely supported: a harness this machine does not have is
// not reading anything, so keeping a file for its sake would leave
// cascade's own file behind on every uninstall. Detection failing is an
// error, never an empty set — "I could not tell" and "nothing else is
// installed" are the two answers this function must never confuse, because
// one of them ends in a deleted file.
func sharedPathsFor(
	ctx context.Context, detector harnessDetector, removing casctx.HarnessKind, cwd string,
) (map[string]string, error) {
	states, err := detector.Detect(ctx)
	if err != nil {
		return nil, err
	}
	generators := harnessGeneratorsByKind()
	shared := map[string]string{}
	for _, state := range states {
		if !state.Detected || state.Kind == removing {
			continue
		}
		generate, ok := generators[state.Kind]
		if !ok {
			continue
		}
		paths, genErr := generate(ctx, cwd)
		if genErr != nil {
			return nil, genErr
		}
		for _, p := range paths {
			shared[p] = string(state.Kind)
		}
	}
	return shared, nil
}

// harnessDetector is the detection seam, so a test can state which
// harnesses are installed instead of installing some.
type harnessDetector interface {
	Detect(ctx context.Context) ([]casctx.HarnessState, error)
}

// hostHarnessDetector builds the detector for the machine this process is
// running on.
//
// Constructed here rather than injected from the caller because a teardown
// is about THIS machine by definition, and a caller passing a detector for
// a different one would be answering a question nobody asked. The unit
// lane swaps it through WithDetector.
//
// Drift recorded: internal/daemon, cmd/cascade's init and its doctor
// mounts each build this same triple with their own pathExists. Four
// spellings of one construction, and this is the fourth — collapsing them
// into one host constructor in internal/context is a change to that
// package, outside this ticket's files_scope, and is worth doing on its
// own.
func hostHarnessDetector() harnessDetector {
	return casctx.NewPathDetector(goruntime.GOOS, os.Getenv, func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

// init wires each harness plugin's shared-path resolver to the real
// computation, the way every other seam in these plugins is wired.
//
// cascade-claude is absent from this list on purpose: it has no uninstall
// subcommand of its own, so a resolver there would be a seam nothing
// reaches. Its teardown lives in this package and is handed the set
// directly.
func init() {
	for _, seam := range []struct {
		what string
		err  error
	}{
		{"cascade-codex", codex.SetSharedPathResolver(
			func(ctx context.Context, cwd string) (codex.SharedPaths, error) {
				return sharedPathsFor(ctx, hostHarnessDetector(), casctx.HarnessCodex, cwd)
			})},
		{"cascade-opencode", opencode.SetSharedPathResolver(
			func(ctx context.Context, cwd string) (opencode.SharedPaths, error) {
				return sharedPathsFor(ctx, hostHarnessDetector(), casctx.HarnessOpenCode, cwd)
			})},
	} {
		if seam.err != nil {
			panic("internal/plugins: wire " + seam.what + " shared-path resolver: " + seam.err.Error())
		}
	}
}
