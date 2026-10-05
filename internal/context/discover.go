package context

import (
	"context"
	"os"
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
// Outputs: a []TierRecord ordered GCI..PAC (ascending Ordinal); a typed
//   cascade.Error only for genuine I/O failure, never for a missing tier.
// Constraints: 02-TARGET-STRUCTURE.md §internal/context; no bare
//   os.UserHomeDir (internal/runtime.PathProvider is the sole exception,
//   per internal/runtime/doc.go — this package takes HomeDirFunc instead);
//   no symlink traversal when reading a tier file; HOME is never crossed
//   by the outward APC/PPC walk.
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

// Discover resolves the context tiers for cwd (or the process's working
// directory, when cwd is ""). It returns GCI, APC, PPC and PRC, then one PAC
// record per directory strictly between the git root and cwd that holds a
// tier file (root-nearest first), then the cwd PAC record. A missing tier
// file or directory comes back as TierRecord{Absent: true}, never an error.
// It returns a typed cascade.Error only when the working directory or a
// present tier file could not be read. Git failures other than "not a
// repository" surface as Findings on the PRC record.
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

	anchor, gitFinding := gitRoot(ctx, resolvedCwd)
	resolvedCwd = alignCwd(anchor, resolvedCwd)
	dirs := tierDirs(resolvedCwd, anchor, homeDir)

	records := make([]TierRecord, 0, len(dirs))
	for i, d := range dirs {
		rec, err := loadTier(d.role, d.dir, i)
		if err != nil {
			if !unreadableAnchor(err, d.dir, anchor, gitFinding) {
				return nil, err
			}
			rec = TierRecord{Role: d.role, Ordinal: i, Dir: d.dir, Absent: true,
				Path: filepath.Join(d.dir, tierDirName, tierFileName)}
		}
		if gitFinding != "" && d.role == TierPRC {
			rec.Findings = append(rec.Findings, gitFinding)
		}
		records = append(records, rec)
	}
	return records, nil
}

// unreadableAnchor reports whether err is the permission failure of reading
// the anchor's tier file after git already reported the anchor unreadable.
// That tier is Absent and the PRC record carries FindingGitPermission, so an
// unreadable cwd surfaces as a finding instead of a bare error.
func unreadableAnchor(err error, dir, anchor string, gitFinding DiscoverFinding) bool {
	kind, ok := cascade.KindOf(err)
	return ok && kind == cascade.KindPermissionDenied &&
		gitFinding == FindingGitPermission && dir == anchor
}

// alignCwd reconciles a symlinked cwd with the git root, which git always
// reports as a real path. When cwd does not sit lexically at or under the
// anchor, its real path is used instead if that one does, so the PAC chain
// between the root and a cwd reached through a link stays visible. Any other
// cwd is returned unchanged.
func alignCwd(anchor, cwd string) string {
	if anchor == cwd || isProperAncestor(anchor, cwd) {
		return cwd
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return cwd
	}
	if resolved == anchor || isProperAncestor(anchor, resolved) {
		return resolved
	}
	return cwd
}

// resolveCwd turns cwd into a clean absolute path, deferring to
// os.Getwd when cwd is "". It never resolves symlinks (filepath.Abs does
// not touch the filesystem); alignCwd does that only when the git root
// requires it.
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

// tierDir pairs a tier role with its candidate directory ("" when the tier
// has no candidate at all, e.g. HOME could not be determined).
type tierDir struct {
	role TierRole
	dir  string
}

// tierDirs computes the tiers' candidate directories from the resolved
// anchor (the git root, or cwd when there was none) and cwd.
//
// # Role-anchored, not positional (the T-P8-45 defect this replaces)
//
// A prior design walked upward from cwd and handed the Nth ancestor with a
// tier marker the Nth role, so an absent intermediate tier shifted every tier
// below it. Here each tier is computed from its role's fixed relationship to
// the anchor: PRC is the anchor; PPC and APC are its parent and grandparent;
// GCI is HOME; PAC is every directory strictly below the anchor down to cwd
// that holds a tier file, plus cwd itself when it is below the anchor. Only
// the PAC role repeats; a tier with no valid candidate is simply absent.
func tierDirs(cwd, anchor string, homeDir HomeDirFunc) []tierDir {
	home := resolveHome(homeDir)

	prc := anchor
	ppcRaw := parentDir(prc)
	apcRaw := parentDir(ppcRaw)

	out := []tierDir{
		{TierGCI, home},
		{TierAPC, boundaryFilter(apcRaw, home)},
		{TierPPC, boundaryFilter(ppcRaw, home)},
		{TierPRC, prc},
	}
	for _, d := range chainBelowRoot(prc, cwd) {
		out = append(out, tierDir{TierPAC, d})
	}
	var pac string
	if cwd != prc && isProperAncestor(prc, cwd) {
		pac = cwd
	}
	return append(out, tierDir{TierPAC, pac})
}

// chainBelowRoot returns every directory D with root < D < cwd that holds a
// tier file, root-nearest first. Empty unless cwd is below root.
func chainBelowRoot(root, cwd string) []string {
	if !isProperAncestor(root, cwd) {
		return nil
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	var out []string
	dir := root
	for _, p := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, p)
		if _, err := os.Lstat(filepath.Join(dir, tierDirName, tierFileName)); err == nil {
			out = append(out, dir)
		}
	}
	return out
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
// HOME itself (that is GCI's slot, not APC's or PPC's) or a proper
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
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
