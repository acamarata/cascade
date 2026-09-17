package claude

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Purpose: resolve the three normative harness config paths, and install
//   the harness instruction golden atomically and idempotently.
// Inputs: a working directory; the process environment (through an
//   injectable Env, never read by probing the filesystem); the
//   package-level Generate variable, which internal/plugins/registry.go
//   sets to a real adapter over internal/context's CC instruction writer
//   (the E/S-08.T3 generator).
// Outputs: one InstallResult per generated file; a Paths value.
// Constraints: plugins/** may import pkg/** only, never internal/**
//   (Art.10.2, plugins-providers-boundary depguard rule) — see the
//   GENERATOR SEAM note in plugins/opencode/install.go, which this package
//   follows exactly. Windows is tier-2: its paths resolve (so the branch is
//   real and testable) but install refuses.
// SPORT: plugins/claude install (ADD) — P1-E16-W4-S34-T1.

// # GENERATOR SEAM
//
// The contract says install.go "retrieves the byte-stable CC instruction
// golden produced by internal/context". That generator's types live in
// internal/context, which this package is denied from importing.
// GeneratedFile and GeneratorFunc are this package's own internal/-free
// vocabulary for the same information; internal/plugins/registry.go — free
// to import both sides — adapts the real writer into this shape at
// registration time. Generate defaults to unwiredGenerator so a build that
// omits that wiring fails loudly rather than installing nothing.

// GeneratedFile is one instruction file the injected generator produced.
type GeneratedFile struct {
	// Path is the absolute filesystem path to materialize Content at.
	Path string
	// Content is the exact bytes to write.
	Content []byte
}

// GeneratorFunc produces the harness instruction golden for cwd. A real
// implementation is deterministic: the same cwd yields byte-identical
// output on every call.
type GeneratorFunc func(ctx context.Context, cwd string) ([]GeneratedFile, error)

// Generate is the active generator. internal/plugins/registry.go overwrites
// it via SetGenerator with a real adapter over internal/context at process
// boot; tests overwrite it directly.
var Generate GeneratorFunc = unwiredGenerator

// unwiredGenerator is Generate's default: a real, typed error reporting
// that no host has wired a generator yet.
func unwiredGenerator(context.Context, string) ([]GeneratedFile, error) {
	return nil, fmt.Errorf("cascade-claude: instruction generator not wired (internal/plugins/registry.go must call claude.SetGenerator)")
}

// SetGenerator installs g as the active generator. A nil g is refused
// rather than silently disabling install.
func SetGenerator(g GeneratorFunc) error {
	if g == nil {
		return fmt.Errorf("cascade-claude: SetGenerator: generator must not be nil")
	}
	Generate = g
	return nil
}

// InstallResult reports what happened to one file this plugin manages.
type InstallResult struct {
	// Path is the file considered.
	Path string
	// Changed is true when the file was created or its content differed
	// from what was already on disk; false when the write was skipped
	// because the content already matched (idempotent no-op).
	Changed bool
	// Preserved is true when the file was left alone because somebody
	// had edited it. It is not an error and not a change: the operator's
	// edit is the more valuable of the two, and the caller REPORTS this
	// rather than resolving it.
	Preserved bool
	// Reason states why a file was preserved. Empty otherwise.
	Reason string
}

// InstructionWriterFunc materializes one generated instruction file.
//
// A seam, like Generate, because the rule for these files lives in
// internal/context (which this package may not import, Art.10.2): an
// instruction file carries a MANAGED BLOCK, and a regeneration replaces
// that block while leaving everything the operator wrote around it
// untouched. A plain whole-file write destroys those edits -- which is
// what this package did until the S-35.T5 acceptance suite ran a second
// `cascade init` over a hand-edited file and watched the edit disappear.
type InstructionWriterFunc func(path string, content []byte) (InstallResult, error)

// WriteInstructionFile is the active writer. internal/plugins wires it to
// the managed-block writer; the default below is the whole-file write,
// which is correct only for a file this plugin fully owns.
var WriteInstructionFile InstructionWriterFunc = wholeFileWriter

// SetInstructionWriter installs w as the active writer. A nil w is
// refused rather than silently reverting to the destructive default.
func SetInstructionWriter(w InstructionWriterFunc) error {
	if w == nil {
		return fmt.Errorf("cascade-claude: SetInstructionWriter: writer must not be nil")
	}
	WriteInstructionFile = w
	return nil
}

// wholeFileWriter is WriteInstructionFile's default.
func wholeFileWriter(path string, content []byte) (InstallResult, error) {
	changed, err := writeIfChanged(path, content)
	return InstallResult{Path: path, Changed: changed}, err
}

// Install renders the harness instruction golden for cwd via Generate and
// materializes every file atomically, skipping any file whose on-disk
// content already matches. A second run reports Changed=false for
// everything and touches no file's mtime.
func Install(ctx context.Context, cwd string) ([]InstallResult, error) {
	files, err := Generate(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("cascade-claude: generate instructions: %w", err)
	}
	results := make([]InstallResult, 0, len(files))
	for _, f := range files {
		res, err := WriteInstructionFile(f.Path, f.Content)
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// writeIfChanged writes content to path only when it differs from what is
// already there, through a temp file in the same directory so a crash
// mid-write leaves either the old file intact or the new one, never a
// truncated hybrid.
func writeIfChanged(path string, content []byte) (bool, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // path is composed by the generator or the resolved config root, not user input.
	switch {
	case err == nil:
		if bytes.Equal(existing, content) {
			return false, nil
		}
	case !os.IsNotExist(err):
		return false, fmt.Errorf("cascade-claude: read %s: %w", path, err)
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
		return fmt.Errorf("cascade-claude: create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".cascade-claude-*")
	if err != nil {
		return fmt.Errorf("cascade-claude: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: close %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: set mode on %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cascade-claude: replace %s: %w", path, err)
	}
	return nil
}

// runInstall is RunCommand's "install" handler: it installs into the
// process's real working directory and config root. Success is silent,
// matching the cascade-opencode idiom — this package may not write to
// stdout directly (internal/build/outputgate.go exempts only
// plugins/examples). A caller needing per-file detail calls the capability
// functions directly.
func runInstall(ctx context.Context, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cascade-claude: getwd: %w", err)
	}
	_, err = InstallAll(ctx, cwd)
	return err
}

// InstallAll performs every part of wiring this harness: the instruction
// files, the hook pack, and the MCP server entry. It returns one
// InstallResult per file it considered, whether or not that file changed.
//
// Exported because `cascade init` step 6 needs exactly this, and was
// calling Install alone -- which writes the instruction files and nothing
// else. The wizard then printed "wired" for a harness with no hook pack
// and no MCP entry, and `cascade context harness list` reported
// cascade_registered=false straight after a successful setup run. One
// spelling of "wire this harness", reached from both the subcommand and
// the wizard, is what keeps those two from drifting apart again.
//
// The order is deliberate: instructions first, because they are the part
// that works with no daemon and no PATH entry, then the two that depend
// on the installed binary being findable. A failure at any step returns
// what was done so far rather than discarding it -- a half-wired harness
// the operator can see beats a half-wired harness reported as nothing.
func InstallAll(ctx context.Context, cwd string) ([]InstallResult, error) {
	results, err := Install(ctx, cwd)
	if err != nil {
		return results, err
	}
	paths, err := HostPaths()
	if err != nil {
		return results, err
	}
	hooks, err := InstallHookPack(paths)
	results = append(results, hooks...)
	if err != nil {
		return results, err
	}
	mcp, err := RegisterMCP(paths)
	results = append(results, mcp)
	return results, err
}
