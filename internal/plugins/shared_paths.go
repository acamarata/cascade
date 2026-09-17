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
	"errors"
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

// sharedPathsFor returns the paths some other harness would generate for
// cwd, mapped to the REASON the file must be kept.
//
// Installed, not merely supported: a harness this machine does not have is
// not reading anything, so keeping a file for its sake would leave
// cascade's own file behind on every uninstall. Detection failing is never
// an empty set — "I could not tell" and "nothing else is installed" are the
// two answers this function must never confuse, because one of them ends in
// a deleted file (R-14.265).
//
// TIER-2 IS THE THIRD ANSWER (R-14.267). On a platform whose harness paths
// this build does not resolve, Detect refuses with
// ErrHarnessDetectionUnsupported. Returning that refusal made every
// uninstall on such a platform fail before removing anything — which is not
// what "I could not tell" should cost. Here it degrades to the conservative
// set: every OTHER supported harness is treated as a possible reader, and
// the reason SAYS the detection was unavailable rather than claiming an
// installation nobody verified. An operator who knows better deletes one
// file; the alternative silently de-configures a harness they kept. Every
// other detector error is still an error.
func sharedPathsFor(
	ctx context.Context, detector harnessDetector, removing casctx.HarnessKind, cwd string,
) (map[string]string, error) {
	states, err := detector.Detect(ctx)
	if err != nil {
		if !errors.Is(err, casctx.ErrHarnessDetectionUnsupported) {
			return nil, err
		}
		return undetectableSharedPaths(ctx, removing, cwd)
	}
	shared := map[string]string{}
	for _, state := range states {
		if !state.Detected || state.Kind == removing {
			continue
		}
		if err := addHarnessPaths(ctx, shared, state.Kind, cwd, installedReason(state.Kind)); err != nil {
			return nil, err
		}
	}
	return shared, nil
}

// undetectableSharedPaths is the tier-2 set: every supported harness other
// than the one being removed, each path carrying a reason that names the
// platform limit instead of asserting an install.
func undetectableSharedPaths(
	ctx context.Context, removing casctx.HarnessKind, cwd string,
) (map[string]string, error) {
	shared := map[string]string{}
	for kind := range harnessGeneratorsByKind() {
		if kind == removing {
			continue
		}
		if err := addHarnessPaths(ctx, shared, kind, cwd, undetectableReason(kind)); err != nil {
			return nil, err
		}
	}
	return shared, nil
}

// addHarnessPaths records every path kind generates for cwd under reason.
func addHarnessPaths(
	ctx context.Context, into map[string]string, kind casctx.HarnessKind, cwd, reason string,
) error {
	generate, ok := harnessGeneratorsByKind()[kind]
	if !ok {
		return nil
	}
	paths, err := generate(ctx, cwd)
	if err != nil {
		return err
	}
	for _, p := range paths {
		into[p] = reason
	}
	return nil
}

// installedReason is what an operator reads when detection worked.
func installedReason(kind casctx.HarnessKind) string {
	return "the " + string(kind) + " harness is installed and still reads this file"
}

// undetectableReason is what an operator reads when it did not. It states
// the limit and the remedy, because "kept" with no way to act on it is the
// bare boolean R-14.265 replaced.
func undetectableReason(kind casctx.HarnessKind) string {
	return "this platform cannot detect installed harnesses (tier-2), and the " + string(kind) +
		" harness writes this same file; delete it by hand if that harness is not installed"
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
