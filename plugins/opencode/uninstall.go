package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Purpose: the uninstall half of the cascade-opencode lifecycle. Destructive
//   code: it must remove only what install itself placed, must be
//   idempotent, and must refuse rather than guess at any path it cannot
//   positively identify as its own.
// Inputs: a working directory; the same Generate variable install.go uses,
//   so uninstall always targets exactly the paths install would (or did)
//   write.
// Outputs: one UninstallResult per candidate path.
// Constraints: the only file this package will ever remove is one whose
//   base name is exactly agentsFileName ("AGENTS.md") - see isManagedPath.
//   That guard holds even if Generate is misconfigured or compromised: a
//   generator that returns some other path is refused, not obeyed, and
//   os.Remove (never RemoveAll) means a directory can never be the target
//   in the first place.
// SPORT: plugins/opencode uninstall (ADD) - P1-E16-W4-S34-T3.

// agentsFileName is the instruction file name every path this plugin is
// permitted to remove must end with. It is the one hard-coded safety fact
// isManagedPath checks against, independent of whatever Generate returns.
const agentsFileName = "AGENTS.md"

// UninstallResult reports what happened to one candidate path.
type UninstallResult struct {
	// Path is the file considered.
	Path string
	// Removed is true when the file existed and was deleted.
	Removed bool
	// AlreadyClean is true when the file did not exist (idempotent no-op).
	AlreadyClean bool
}

// isManagedPath reports whether path is a shape cascade-opencode is ever
// permitted to delete: its base name must be exactly agentsFileName. This
// is a hard, unconditional guard - it is checked before Generate's answer
// is trusted for anything destructive, so a generator bug or a future
// caller passing an arbitrary path can never turn Uninstall into a general
// file-deletion primitive.
func isManagedPath(path string) bool {
	return path != "" && filepath.Base(path) == agentsFileName
}

// Uninstall removes exactly the files Generate says cascade-opencode would
// install for cwd today, refusing (not touching, not guessing) any
// candidate whose shape isManagedPath rejects. Idempotent: a file that is
// already absent is reported AlreadyClean, not an error.
func Uninstall(ctx context.Context, cwd string) ([]UninstallResult, error) {
	files, err := Generate(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("cascade-opencode: generate instructions: %w", err)
	}
	results := make([]UninstallResult, 0, len(files))
	for _, f := range files {
		res, err := removeManaged(f.Path)
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}

// removeManaged deletes path if and only if isManagedPath accepts it and
// the file exists. A path isManagedPath rejects is a refusal (typed
// error), never a silent skip, so a caller cannot mistake "refused" for
// "already clean".
func removeManaged(path string) (UninstallResult, error) {
	if !isManagedPath(path) {
		return UninstallResult{}, fmt.Errorf(
			"cascade-opencode: uninstall: refusing unrecognized path %q (must be named %s)", path, agentsFileName)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return UninstallResult{Path: path, AlreadyClean: true}, nil
		}
		return UninstallResult{}, fmt.Errorf("cascade-opencode: stat %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return UninstallResult{}, fmt.Errorf("cascade-opencode: remove %s: %w", path, err)
	}
	return UninstallResult{Path: path, Removed: true}, nil
}

// runUninstall is RunCommand's "uninstall" handler. Success is silent
// (nil), matching runInstall's rationale above.
func runUninstall(ctx context.Context, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cascade-opencode: getwd: %w", err)
	}
	_, err = Uninstall(ctx, cwd)
	return err
}
