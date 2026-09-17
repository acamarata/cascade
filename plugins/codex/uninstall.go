package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Purpose: the uninstall half of the cascade-codex lifecycle. Destructive
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
// SPORT: plugins/codex uninstall (ADD) - P1-E16-W4-S34-T3.

// agentsFileName is the instruction file name every path this plugin is
// permitted to remove must end with. It is the one hard-coded safety fact
// isManagedPath checks against, independent of whatever Generate returns.
const agentsFileName = "AGENTS.md"

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

// SharedPathResolverFunc reports which files another installed harness
// still reads, for cwd.
type SharedPathResolverFunc func(ctx context.Context, cwd string) (SharedPaths, error)

// ResolveSharedPaths is the active resolver, used by this plugin's own
// uninstall subcommand — the one caller that has nobody to ask.
//
// internal/plugins wires it, the way it wires every other seam here. Its
// default REFUSES rather than answering "nothing is shared": that answer
// is the bug this whole mechanism exists to remove, and a host that forgot
// to wire the resolver should find out by being refused, not by silently
// deleting a file the other harness was still reading (R-14.265).
var ResolveSharedPaths SharedPathResolverFunc = unwiredSharedPaths

// SetSharedPathResolver installs r as the active resolver. A nil r is
// refused rather than reverting to the unwired default.
func SetSharedPathResolver(r SharedPathResolverFunc) error {
	if r == nil {
		return fmt.Errorf("cascade-codex: SetSharedPathResolver: resolver must not be nil")
	}
	ResolveSharedPaths = r
	return nil
}

// unwiredSharedPaths is ResolveSharedPaths' default: a real, typed error
// reporting that no host has told this plugin what else is installed.
func unwiredSharedPaths(context.Context, string) (SharedPaths, error) {
	return nil, fmt.Errorf(
		"cascade-codex: uninstall cannot tell which files another installed harness still reads " +
			"(no shared-path resolver is wired); run the uninstall through cascade, " +
			"which computes that from the installed harness set")
}

// isManagedPath reports whether path is a shape cascade-codex is ever
// permitted to delete: its base name must be exactly agentsFileName. This
// is a hard, unconditional guard - it is checked before Generate's answer
// is trusted for anything destructive, so a generator bug or a future
// caller passing an arbitrary path can never turn Uninstall into a general
// file-deletion primitive.
func isManagedPath(path string) bool {
	return path != "" && filepath.Base(path) == agentsFileName
}

// Uninstall removes exactly the files Generate says cascade-codex would
// install for cwd today, refusing (not touching, not guessing) any
// candidate whose shape isManagedPath rejects. Idempotent: a file that is
// already absent is reported AlreadyClean, not an error.
func Uninstall(ctx context.Context, cwd string, shared SharedPaths) ([]UninstallResult, error) {
	files, err := Generate(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("cascade-codex: generate instructions: %w", err)
	}
	results := make([]UninstallResult, 0, len(files))
	for _, f := range files {
		res, err := removeManaged(f.Path, shared)
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
func removeManaged(path string, shared SharedPaths) (UninstallResult, error) {
	if !isManagedPath(path) {
		return UninstallResult{}, fmt.Errorf(
			"cascade-codex: uninstall: refusing unrecognized path %q (must be named %s)", path, agentsFileName)
	}
	if why := shared.Claims(path); why != "" {
		// Checked before the stat, not after: whether another harness
		// reads this file does not depend on whether it happens to be
		// there right now, and asking in that order keeps "kept" from
		// ever being reported as "already clean".
		return UninstallResult{
			Path: path, Kept: true,
			KeptReason: why,
		}, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return UninstallResult{Path: path, AlreadyClean: true}, nil
		}
		return UninstallResult{}, fmt.Errorf("cascade-codex: stat %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return UninstallResult{}, fmt.Errorf("cascade-codex: remove %s: %w", path, err)
	}
	return UninstallResult{Path: path, Removed: true}, nil
}

// runUninstall is RunCommand's "uninstall" handler. Success is silent
// (nil), matching runInstall's rationale above.
func runUninstall(ctx context.Context, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cascade-codex: getwd: %w", err)
	}
	shared, err := ResolveSharedPaths(ctx, cwd)
	if err != nil {
		return err
	}
	_, err = Uninstall(ctx, cwd, shared)
	return err
}
