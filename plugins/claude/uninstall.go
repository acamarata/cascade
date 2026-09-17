// Purpose: cascade-claude's uninstall — the paired half of Install,
//
//	InstallHookPack and RegisterMCP, removing exactly what those three put
//	on disk and nothing else.
//
// WHAT "NOTHING ELSE" MEANS, AND WHY IT IS THE WHOLE DESIGN (R-14.248).
//
//	The harness config directory is the OPERATOR's, not this plugin's.
//	Three rules follow and each is load-bearing:
//
//	  - the config DIRECTORY is never removed. A plugin that deleted a
//	    directory it did not create would take unrelated configuration
//	    with it;
//	  - the MCP server entry is removed by KEY from mcp.json, leaving
//	    every other server's entry intact. Deleting that file would
//	    uninstall every other integration the operator has;
//	  - a file this plugin does not recognise as its own is LEFT and
//	    REPORTED, never deleted on a guess.
//
// IDEMPOTENT, in the same sense Install is. An already-absent file is a
//
//	SUCCESS reporting Removed=false, not an error: a second uninstall must
//	be a no-op, because that is what re-running a teardown after a partial
//	failure looks like.
//
// WHERE THE AUDIT IS. Not here. Art.10.2 forbids plugins/ from importing
//
//	internal/**, and the audit log lives there, so this file returns what
//	it did and internal/plugins writes the row — under KindConfigReload,
//	because what an uninstall does IS a harness configuration change and
//	the fourteen-kind taxonomy stays fourteen (R-14.248).
//
// Inputs: the resolved harness paths, and the cwd whose instruction files
//
//	were installed.
//
// Outputs: one UninstallResult per file considered.
// SPORT: plugins/claude uninstall (ADD) — P1-E16-W4-S34-T1.

package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// UninstallResult reports what happened to one candidate path.
//
// Four outcomes, and an operator needs to tell them apart: Removed means
// the file is gone, AlreadyClean means there was nothing to do, Kept means
// there is still a file on disk that this uninstall deliberately left, and
// neither of the three means the path was refused (that is an error). The
// three harness adapters carry the same shape so a caller handling all of
// them does not need three vocabularies (R-14.265).
type UninstallResult struct {
	// Path is the file considered.
	Path string
	// Removed is true when the file existed and was deleted.
	Removed bool
	// AlreadyClean is true when the file did not exist (idempotent no-op).
	AlreadyClean bool
	// Kept is true when the file exists and was left in place on purpose.
	Kept bool
	// KeptReason says why, in a sentence an operator can act on. Empty
	// unless Kept. A bare boolean tells somebody a file survived and
	// nothing about what to do with it.
	KeptReason string
}

// SharedPaths maps a file this plugin would remove to the REASON it must
// be kept instead — an operator-facing sentence, not a harness name.
//
// It is a PARAMETER, not an option with a default, because the default
// that would be convenient here — "nothing is shared" — is exactly the bug
// this type exists to remove (R-14.265). Every call site has to say
// something, and a caller that genuinely knows of no other harness passes
// an empty set on purpose rather than by omission.
//
// The set is computed by the composition root, which is the only place
// that can see both what is installed and what each install generates.
// This package may not import internal/**, so it cannot compute it, and a
// base-name heuristic on this side would refuse to remove AGENTS.md on a
// machine where only one harness was ever installed.
//
// THE VALUE IS THE REASON, and that is load-bearing (R-14.267). This
// package used to receive a harness name and compose "the X harness is
// installed and still reads this file". On a platform where harness
// detection is unavailable the host knows a file is shared and does NOT
// know that anything is installed, so that sentence is a claim this build
// cannot support. Only the composition root can tell the two cases apart,
// so only the composition root writes the sentence.
type SharedPaths map[string]string

// Claims reports why path must be kept, or "" when nothing claims it.
func (s SharedPaths) Claims(path string) string { return s[path] }

// Uninstall removes every file cascade-claude installs for cwd.
//
// It continues past a file it could not remove, so one stubborn file does
// not leave the rest of the harness half-configured; the first error is
// returned once every other file has been dealt with.
func Uninstall(ctx context.Context, paths Paths, cwd string, shared SharedPaths) ([]UninstallResult, error) {
	results := make([]UninstallResult, 0, 4)
	var firstErr error

	instructions, genErr := uninstallInstructions(ctx, cwd, shared)
	results = append(results, instructions...)
	firstErr = keepFirst(firstErr, genErr)

	for _, name := range []string{hookPackFile, hookPackVersionFile} {
		res, err := removeFile(filepath.Join(paths.HookConfig, name))
		results = append(results, res)
		firstErr = keepFirst(firstErr, err)
	}

	mcpRes, mcpErr := removeMCPEntry(paths.MCPConfig)
	results = append(results, mcpRes)
	firstErr = keepFirst(firstErr, mcpErr)

	return results, firstErr
}

// uninstallInstructions removes the instruction files the generator would
// produce for cwd.
//
// It asks the GENERATOR which files those are rather than guessing from a
// list kept here: a second list would drift from the first, and the way it
// would drift is by leaving a file behind that nobody remembered to add.
// A generator that is not wired is reported, and nothing is removed — this
// plugin will not delete files it cannot confirm it wrote.
func uninstallInstructions(ctx context.Context, cwd string, shared SharedPaths) ([]UninstallResult, error) {
	files, err := Generate(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("cascade-claude: uninstall: cannot determine which instruction files are ours: %w", err)
	}
	results := make([]UninstallResult, 0, len(files))
	var firstErr error
	for _, f := range files {
		res, removeErr := removeIfOurs(f.Path, f.Content, shared)
		results = append(results, res)
		firstErr = keepFirst(firstErr, removeErr)
	}
	return results, firstErr
}

// removeIfOurs deletes path only when its bytes are the ones this plugin
// would have written.
//
// A file whose content has DIVERGED is kept and reported: the operator
// edited it, or something else owns it now, and either way deleting it
// would destroy work this plugin did not do.
func removeIfOurs(path string, ours []byte, shared SharedPaths) (UninstallResult, error) {
	if why := shared.Claims(path); why != "" {
		return UninstallResult{
			Path: path, Kept: true,
			KeptReason: why,
		}, nil
	}
	onDisk, err := os.ReadFile(path) //nolint:gosec // path comes from the generator, not from user input.
	if os.IsNotExist(err) {
		return UninstallResult{Path: path, AlreadyClean: true}, nil
	}
	if err != nil {
		return UninstallResult{Path: path}, fmt.Errorf("cascade-claude: uninstall: read %s: %w", path, err)
	}
	if !bytesEqual(onDisk, ours) {
		return UninstallResult{
			Path: path, Kept: true,
			KeptReason: "the file has been edited since this plugin wrote it",
		}, nil
	}
	if err := os.Remove(path); err != nil {
		return UninstallResult{Path: path}, fmt.Errorf("cascade-claude: uninstall: remove %s: %w", path, err)
	}
	return UninstallResult{Path: path, Removed: true}, nil
}

// removeFile deletes path, treating an already-absent file as done.
//
// The hook-pack files are not content-checked the way instruction files
// are: they are rendered from the live socket path, so their bytes
// legitimately differ between installs, and a content check would keep
// every one of them forever.
func removeFile(path string) (UninstallResult, error) {
	err := os.Remove(path)
	switch {
	case err == nil:
		return UninstallResult{Path: path, Removed: true}, nil
	case os.IsNotExist(err):
		return UninstallResult{Path: path}, nil
	default:
		return UninstallResult{Path: path}, fmt.Errorf("cascade-claude: uninstall: remove %s: %w", path, err)
	}
}

// removeMCPEntry drops this plugin's server from the harness MCP config,
// carrying every other entry through untouched.
//
// mergeMCPConfig with a nil entry is the removal half of the same function
// RegisterMCP adds with, so the two can never disagree about what this
// plugin's entry is. A missing config file is nothing to remove.
func removeMCPEntry(path string) (UninstallResult, error) {
	existing, err := readMCPConfig(path)
	if err != nil {
		return UninstallResult{Path: path}, err
	}
	if len(existing) == 0 {
		return UninstallResult{Path: path}, nil
	}
	present, err := mcpEntryPresent(existing)
	if err != nil {
		return UninstallResult{Path: path}, err
	}
	if !present {
		// Our entry is not in there. The file is left EXACTLY as it was,
		// byte for byte — not re-encoded with identical meaning and a new
		// mtime. mergeMCPConfig canonicalises formatting, which is fine
		// when it is also removing something and wrong when it is not: a
		// changed config an operator never asked for is a change they have
		// to explain.
		return UninstallResult{Path: path}, nil
	}
	merged, err := mergeMCPConfig(existing, nil)
	if err != nil {
		return UninstallResult{Path: path}, err
	}
	changed, err := writeIfChanged(path, merged)
	return UninstallResult{Path: path, Removed: changed}, err
}

// bytesEqual compares two byte slices. Declared here rather than reaching
// for bytes.Equal so this file's imports stay the same small set the rest
// of the package uses.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// keepFirst returns the first non-nil error, so a multi-file uninstall
// reports the FIRST thing that went wrong rather than the last.
func keepFirst(first, next error) error {
	if first != nil {
		return first
	}
	return next
}

// mcpEntryPresent reports whether this plugin's server appears in the
// harness MCP config.
//
// The document is USER-OWNED and untrusted, so a shape this cannot read is
// an error rather than a guess: answering "not present" on an unparseable
// config would leave our entry in place while reporting a clean uninstall.
func mcpEntryPresent(existing []byte) (bool, error) {
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(existing, &doc); err != nil {
		return false, fmt.Errorf("cascade-claude: uninstall: the harness MCP config is not a JSON object: %w", err)
	}
	raw, ok := doc[mcpServersKey]
	if !ok || len(raw) == 0 {
		return false, nil
	}
	servers := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &servers); err != nil {
		return false, fmt.Errorf("cascade-claude: uninstall: the harness MCP server table is not an object: %w", err)
	}
	_, present := servers[MCPServerName]
	return present, nil
}
