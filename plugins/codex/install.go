package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Purpose: the install half of the cascade-codex lifecycle — invoke the
//   injected instruction generator, write its output atomically, and skip
//   unchanged content on a re-run.
// Inputs: a working directory; the package-level Generate variable, which
//   internal/plugins/registry.go sets to a real adapter over
//   internal/context's Codex instruction writer (the E/S-09.T3 generator).
// Outputs: one InstallResult per generated file.
// Constraints: plugins/** may import pkg/** only, never internal/** (Art.
//   10.2, plugins-providers-boundary depguard rule) — internal/context's
//   HarnessFile/WriteHarnessFile types are therefore unreachable from this
//   package by construction. See the GENERATOR SEAM note below for how
//   that boundary is bridged without violating it, and journals/
//   P1-E16-W4-S34-T3.md for the full contradiction writeup.
// SPORT: plugins/codex install (ADD) — P1-E16-W4-S34-T3.

// # GENERATOR SEAM
//
// The ticket contract says install.go "invokes the E/S-09.T3 instruction
// generator" directly. That generator's types (HarnessFile,
// GenerateHarnessInstructions) live in internal/context, which this
// package is denied from importing. GeneratedFile and GeneratorFunc are
// this package's own, internal/-free vocabulary for the same information;
// internal/plugins/registry.go — an internal/ package, free to import both
// sides — adapts internal/context's real writer into this shape at
// registration time. Generate defaults to unwiredGenerator so a build that
// somehow omits the registry.go wiring fails loudly (a real error) rather
// than silently installing nothing.

// GeneratedFile is one instruction file the injected generator produced:
// the absolute path to write it at, and its exact bytes.
type GeneratedFile struct {
	// Path is the absolute filesystem path to materialize Content at.
	Path string
	// Content is the exact bytes to write.
	Content []byte
}

// GeneratorFunc produces the Codex instruction golden for cwd. A real
// implementation is deterministic: the same cwd (and the context state it
// reads) yields byte-identical output on every call.
type GeneratorFunc func(ctx context.Context, cwd string) ([]GeneratedFile, error)

// Generate is the active generator. internal/plugins/registry.go overwrites
// it via SetGenerator with a real adapter over internal/context at process
// boot; tests overwrite it directly to exercise install/uninstall without
// a real Context Engine.
var Generate GeneratorFunc = unwiredGenerator

// unwiredGenerator is Generate's default: a real, typed error (not a
// silent no-op) reporting that no host has wired a generator yet.
func unwiredGenerator(context.Context, string) ([]GeneratedFile, error) {
	return nil, fmt.Errorf("cascade-codex: instruction generator not wired (internal/plugins/registry.go must call codex.SetGenerator)")
}

// SetGenerator installs g as the active generator. A nil g is refused
// (Generate is left unchanged) rather than silently disabling install.
func SetGenerator(g GeneratorFunc) error {
	if g == nil {
		return fmt.Errorf("cascade-codex: SetGenerator: generator must not be nil")
	}
	Generate = g
	return nil
}

// InstallResult reports what happened to one generated file.
type InstallResult struct {
	// Path is the file considered.
	Path string
	// Changed is true when the file was created or its content differed
	// from what is already on disk; false when the write was skipped
	// because the content already matched (idempotent no-op).
	Changed bool
}

// Install renders the Codex instruction golden for cwd via Generate and
// materializes every file atomically, skipping any file whose on-disk
// content already matches (idempotent: a second run reports Changed=false
// for everything and touches no file's mtime).
func Install(ctx context.Context, cwd string) ([]InstallResult, error) {
	files, err := Generate(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("cascade-codex: generate instructions: %w", err)
	}
	results := make([]InstallResult, 0, len(files))
	for _, f := range files {
		changed, err := writeIfChanged(f.Path, f.Content)
		if err != nil {
			return results, err
		}
		results = append(results, InstallResult{Path: f.Path, Changed: changed})
	}
	return results, nil
}

// writeIfChanged writes content to path only when it differs from what is
// already there, through a temp file in the same directory so a crash
// mid-write leaves either the old file intact or the new one, never a
// truncated hybrid of both.
func writeIfChanged(path string, content []byte) (bool, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // path is composed by the generator from discovered tier roots, not user input.
	switch {
	case err == nil:
		if bytes.Equal(existing, content) {
			return false, nil
		}
	case !os.IsNotExist(err):
		return false, fmt.Errorf("cascade-codex: read %s: %w", path, err)
	}
	if err := writeAtomic(path, content); err != nil {
		return false, err
	}
	return true, nil
}

// writeAtomic writes data to path via create-temp-then-rename in path's own
// directory.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cascade-codex: create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".cascade-codex-*")
	if err != nil {
		return fmt.Errorf("cascade-codex: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-codex: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-codex: close %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-codex: set mode on %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-codex: replace %s: %w", path, err)
	}
	return nil
}

// runInstall is RunCommand's "install" handler: it installs into the
// process's real working directory. Success is silent (nil), matching
// plugins/pbd's RunCommand idiom - this package may not write to stdout
// directly (internal/build/outputgate.go exempts only plugins/examples).
// A caller that needs per-file detail calls Install directly.
func runInstall(ctx context.Context, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cascade-codex: getwd: %w", err)
	}
	_, err = Install(ctx, cwd)
	return err
}
