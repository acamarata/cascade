package context

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: role-anchored discovery of the five context tiers. Locates each
//   tier's directory from a git-root anchor (never by walking N ancestors
//   and handing out roles positionally — that defect is documented at
//   length below and is the reason this file exists) and reads its
//   instruction file, if present.
// Inputs: a context.Context (for the git subprocess), a working directory
//   ("" defers to os.Getwd), and an injectable HomeDirFunc.
// Outputs: a []TierRecord ordered GCI..PAI (ascending Ordinal); a typed
//   cascade.Error only for genuine I/O failure, never for a missing tier.
// Constraints: 02-TARGET-STRUCTURE.md §internal/context; no bare
//   os.UserHomeDir (internal/runtime.PathProvider is the sole exception,
//   per internal/runtime/doc.go — this package takes HomeDirFunc instead);
//   no symlink traversal when reading a tier file; HOME is never crossed
//   by the outward ASI/PPI walk.
// SPORT: context-engine/discovery (ADD, per T-1 sport_updates).

// tierDirName and tierFileName are the fixed, product-agnostic location of
// a tier's instruction file within its directory: <dir>/.cascade/CASCADE.md.
// Core never knows a downstream harness's own file naming (CLAUDE.md,
// AGENTS.md, ...) — that translation belongs to the harness-generation
// layer, not discovery.
const (
	tierDirName  = ".cascade"
	tierFileName = "CASCADE.md"
)

// HomeDirFunc matches os.UserHomeDir's signature, so callers can inject a
// fake home directory in tests instead of touching the real one (Art.7.1).
// Production callers pass os.UserHomeDir explicitly (a nil HomeDirFunc
// falls back to it) — the explicit pass, not a silent default, is what
// keeps "the only place allowed to call os.UserHomeDir" honest as
// internal/runtime's composition root, not this package.
type HomeDirFunc func() (string, error)

// Discover resolves the five context tiers for cwd (or the process's
// working directory, when cwd is ""). It never returns an error for a
// missing tier file or a missing tier directory — those come back as
// TierRecord{Absent: true}. It returns a typed cascade.Error only when the
// working directory or a present tier file could not be read.
func Discover(ctx context.Context, cwd string, homeDir HomeDirFunc) ([]TierRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}

	resolvedCwd, err := resolveCwd(cwd)
	if err != nil {
		return nil, err
	}

	anchor := gitRoot(ctx, resolvedCwd)
	dirs := tierDirs(resolvedCwd, anchor, homeDir)

	records := make([]TierRecord, 0, len(dirs))
	for i, d := range dirs {
		rec, err := loadTier(d.role, d.dir, i)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// resolveCwd turns cwd into a clean absolute path, deferring to
// os.Getwd when cwd is "". It never resolves symlinks (filepath.Abs does
// not touch the filesystem), matching the no-symlink-traversal invariant.
func resolveCwd(cwd string) (string, error) {
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", cascade.Wrap(cascade.KindUnavailable, err, "context: resolve working directory")
		}
		cwd = wd
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindInvalidInput, err, "context: resolve absolute path for %q", cwd)
	}
	return filepath.Clean(abs), nil
}

// gitRoot invokes `git rev-parse --show-toplevel` in cwd and returns the
// repository root. On any failure — git not on PATH, cwd not inside a
// repository, the subprocess erroring — it falls back to cwd itself, per
// the ticket's explicit "both paths are tested" contract; this is never a
// cascade.Error, only a fallback.
//
// git always prints the toplevel with forward slashes, even on Windows
// (verified: `git rev-parse --show-toplevel` on git-for-windows emits
// "C:/Users/..."); filepath.FromSlash converts that to the platform
// separator before any filepath comparison is done against it.
func gitRoot(ctx context.Context, cwd string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return cwd
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return cwd
	}
	return filepath.Clean(filepath.FromSlash(root))
}

// tierDir pairs a tier role with its candidate directory ("" when the tier
// has no candidate at all, e.g. HOME could not be determined).
type tierDir struct {
	role TierRole
	dir  string
}

// tierDirs computes the five tiers' candidate directories from the
// resolved anchor (the git root, or cwd when there was none) and cwd.
//
// # Role-anchored, not positional (the T-P8-45 defect this replaces)
//
// A prior design walked the filesystem upward from cwd and handed the Nth
// ancestor that had a tier marker the Nth role from an ordered list. With
// an intermediate tier absent from the machine, every tier below it shifted
// up one slot and silently mislabeled itself. This implementation instead
// computes each tier directly from its role's fixed relationship to the
// anchor: PRI is the anchor; PPI and ASI are the anchor's parent and
// grandparent; GCI is HOME; PAI is cwd, only when cwd is strictly below the
// anchor. A tier with no valid candidate is simply absent — it never
// bumps a neighbour into its slot.
func tierDirs(cwd, anchor string, homeDir HomeDirFunc) []tierDir {
	home := resolveHome(homeDir)

	pri := anchor
	ppiRaw := parentDir(pri)
	asiRaw := parentDir(ppiRaw)

	ppi := boundaryFilter(ppiRaw, home)
	asi := boundaryFilter(asiRaw, home)

	var pai string
	if cwd != pri && isProperAncestor(pri, cwd) {
		pai = cwd
	}

	// No further de-duplication pass is needed here: boundaryFilter has
	// already resolved the only possible collision (an ASI/PPI candidate
	// landing on HOME). PRI, PAI, and GCI are each computed independently
	// of one another and can never coincide with a sibling tier's
	// directory by construction — including the case where PRI itself
	// happens to equal HOME, which is a legitimate fact about the
	// filesystem (the repo root IS the home directory), not a discovery
	// defect, and must not suppress PRI the way v1's uniform "claimed" set
	// would have.
	return []tierDir{
		{TierGCI, home},
		{TierASI, asi},
		{TierPPI, ppi},
		{TierPRI, pri},
		{TierPAI, pai},
	}
}

// resolveHome returns the clean, absolute HOME directory, or "" when
// homeDir could not determine one. A missing HOME is not an error — it
// simply leaves GCI (and anything the boundary guard measures against it)
// absent.
func resolveHome(homeDir HomeDirFunc) string {
	h, err := homeDir()
	if err != nil || h == "" {
		return ""
	}
	abs, err := filepath.Abs(h)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// boundaryFilter drops dir when it would cross the HOME boundary: dir is
// HOME itself (that is GCI's slot, not ASI's or PPI's) or a proper
// ancestor of HOME (the outward walk has overshot HOME entirely). Every
// other dir, including one that shares no ancestry with HOME at all, is
// returned unchanged.
func boundaryFilter(dir, home string) string {
	if dir == "" || home == "" {
		return dir
	}
	if dir == home || isProperAncestor(dir, home) {
		return ""
	}
	return dir
}

// parentDir returns dir's parent, or "" when dir has no parent (it is a
// filesystem root, where filepath.Dir is idempotent).
func parentDir(dir string) string {
	if dir == "" {
		return ""
	}
	parent := filepath.Dir(dir)
	if parent == dir {
		return ""
	}
	return parent
}

// isProperAncestor reports whether ancestor is a strict ancestor directory
// of descendant (descendant is nested under it, and they are not equal).
func isProperAncestor(ancestor, descendant string) bool {
	if ancestor == "" || descendant == "" || ancestor == descendant {
		return false
	}
	rel, err := filepath.Rel(ancestor, descendant)
	if err != nil {
		return false
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}

// loadTier builds the TierRecord for role at dir, reading its instruction
// file when one is present. A dir of "" produces an Absent record with no
// filesystem access at all.
func loadTier(role TierRole, dir string, ordinal int) (TierRecord, error) {
	rec := TierRecord{Role: role, Ordinal: ordinal}
	if dir == "" {
		rec.Absent = true
		return rec, nil
	}
	rec.Dir = dir
	rec.Path = filepath.Join(dir, tierDirName, tierFileName)

	info, err := os.Lstat(rec.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		rec.Absent = true
		return rec, nil
	case err != nil:
		return TierRecord{}, wrapTierFSErr(err, "stat tier file "+rec.Path)
	}

	// No symlink traversal: a tier file that is itself a symlink (or a
	// directory, which cannot be an instruction file) is treated as absent
	// rather than followed or opened.
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		rec.Absent = true
		return rec, nil
	}

	content, err := os.ReadFile(rec.Path)
	if err != nil {
		return TierRecord{}, wrapTierFSErr(err, "read tier file "+rec.Path)
	}
	rec.Content = string(content)
	return rec, nil
}

// wrapTierFSErr classifies a filesystem error into the taxonomy: permission
// errors become KindPermissionDenied, everything else KindUnavailable —
// mirroring internal/daemon/service.wrapFSError's convention for the same
// distinction.
func wrapTierFSErr(err error, msg string) error {
	if os.IsPermission(err) {
		return cascade.Wrap(cascade.KindPermissionDenied, err, "context: "+msg)
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "context: "+msg)
}
