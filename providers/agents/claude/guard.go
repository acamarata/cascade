// Purpose: the filesystem and environment guards Spawn runs before any job
//   child exists: the SpawnEnv custody check (R-21.151), the lease and
//   --add-dir validation, the runtime-home/tree disjointness and symlink
//   escape walk (R-21.177), the refusal of repo-owned harness settings, and
//   the symlink-refusing hook-pack copy.
// Inputs: a provider.SpawnEnv and Profile; a Lease; the hook-pack source.
// Outputs: nil or a typed refusal; the tree-relative paths the copy wrote.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   from driver.go/exec.go to hold the 300-line cap. Every check is
//   fail-closed: an unreadable or unresolvable path refuses the spawn.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrSpawnEnvRejected refuses a SpawnEnv or runtime home that breaks the
// R-21.151 custody rules. ErrWorktreeEscape refuses a leased tree that
// exposes a path outside itself. ErrForeignHarnessSettings refuses a tree
// whose .claude/ holds a harness settings file the hook pack does not
// write: its hooks would run with the vendor auth environment.
var (
	ErrSpawnEnvRejected       = cascade.New(cascade.KindPermissionDenied, "claude: spawn environment rejected")
	ErrWorktreeEscape         = cascade.New(cascade.KindCapabilityDenied, "claude: leased tree exposes a path outside itself")
	ErrForeignHarnessSettings = cascade.New(cascade.KindCapabilityDenied, "claude: leased tree carries harness settings the hook pack does not write")
	// ErrWorktreeRootMismatch refuses a spec.WorktreeRoot that is not the
	// lease root: the driver never silently prefers one of the two.
	ErrWorktreeRootMismatch = cascade.New(cascade.KindConflict, "claude: spec.WorktreeRoot is not the leased tree's root")
)

func envRejected(format string, args ...any) error {
	return cascade.Wrapf(cascade.KindPermissionDenied, ErrSpawnEnvRejected, "claude: "+format, args...)
}

// checkSpawnEnv refuses unless env.Driver is non-empty (else
// ErrEntitlement), every Driver key is in DriverEnvAllowlist, HOME equals
// env.Home and the profile's RuntimeHome, the vendor variable points under
// that home, the profile's one key variable (B-1) is set beside it, and
// every Tool key is in ToolEnvAllowlist without either.
func checkSpawnEnv(env provider.SpawnEnv, prof Profile) error {
	if len(env.Driver) == 0 {
		return provider.ErrEntitlement
	}
	if prof.VendorAuthVar != VendorConfigDirVar {
		return envRejected("profile vendor auth variable %q is not this driver's", prof.VendorAuthVar)
	}
	keyVar, err := keyVarFor(prof)
	if err != nil {
		return err
	}
	values, err := parsePairs(env.Driver, provider.DriverEnvAllowlist(prof.VendorAuthVar, keyVar))
	if err != nil {
		return err
	}
	if _, err := parsePairs(env.Tool, provider.ToolEnvAllowlist()); err != nil {
		return err
	}
	home := values["HOME"]
	if home == "" || home != env.Home || home != prof.RuntimeHome || !filepath.IsAbs(home) {
		return envRejected("HOME %q must equal SpawnEnv.Home %q and the profile runtime home %q", home, env.Home, prof.RuntimeHome)
	}
	if vendor := values[prof.VendorAuthVar]; vendor == "" || !strictlyUnder(home, vendor) {
		return envRejected("the vendor auth variable must name a directory under the runtime home")
	}
	if len(values[keyVar]) < minKeyValueLen {
		return envRejected("the key variable %s must be set beside %s (at least %d bytes)", keyVar, prof.VendorAuthVar, minKeyValueLen)
	}
	if strings.ContainsAny(home, " \t\n,()") {
		return envRejected("runtime home %q holds a character the harness rule lists split on", home)
	}
	return checkRuntimeHome(home)
}

// parsePairs splits KEY=VALUE pairs, refusing a missing '=', an empty or
// duplicate key, or a key outside allow.
func parsePairs(pairs, allow []string) (map[string]string, error) {
	allowed := make(map[string]bool, len(allow))
	for _, k := range allow {
		allowed[k] = true
	}
	out := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, envRejected("malformed environment entry")
		}
		if !allowed[key] {
			return nil, envRejected("key %q is outside the allowlist", key)
		}
		if _, dup := out[key]; dup {
			return nil, envRejected("key %q appears twice", key)
		}
		out[key] = val
	}
	return out, nil
}

// checkRuntimeHome requires an existing, non-symlink directory that no
// group or other user can read (R-21.151: mode 0700, driver-only).
func checkRuntimeHome(home string) error {
	fi, err := os.Lstat(home)
	if err != nil {
		return envRejected("runtime home: %v", err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return envRejected("runtime home %q is not a real directory", home)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return envRejected("runtime home %q has mode %v, want 0700", home, fi.Mode().Perm())
	}
	return nil
}

// checkLease requires an absolute root that is a directory and an absolute
// git directory taken from the lease record.
func checkLease(l Lease) error {
	if !filepath.IsAbs(l.Root) || !filepath.IsAbs(l.GitDir) {
		return cascade.New(cascade.KindInvalidInput, "claude: lease root and git dir must be absolute")
	}
	fi, err := os.Stat(l.Root)
	if err != nil || !fi.IsDir() {
		return cascade.Newf(cascade.KindInvalidInput, "claude: lease root %q is not a directory", l.Root)
	}
	return nil
}

// addDirs maps each in-scope prefix to its --add-dir path under root,
// refusing an absolute, empty or escaping prefix.
func addDirs(root string, prefixes []string) ([]string, error) {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		clean := filepath.Clean(p)
		if p == "" || filepath.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, cascade.Newf(cascade.KindInvalidInput, "claude: in-scope prefix %q must stay inside the tree", p)
		}
		out = append(out, filepath.Join(root, clean))
	}
	return out, nil
}

// guardTree refuses when the runtime home and the tree overlap, when any
// symlink sits under .claude/, or when any symlink resolves outside root.
func guardTree(root, home string) error {
	rootR, err := filepath.EvalSymlinks(root)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "claude: resolving lease root")
	}
	homeR, err := filepath.EvalSymlinks(home)
	if err != nil {
		return envRejected("resolving runtime home: %v", err)
	}
	if within(rootR, homeR) || within(homeR, rootR) {
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: runtime home and leased tree overlap")
	}
	return filepath.WalkDir(root, func(path string, de fs.DirEntry, werr error) error {
		if werr != nil {
			return cascade.Wrapf(cascade.KindInternal, werr, "claude: walking the leased tree")
		}
		if de.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == ".claude" || strings.HasPrefix(rel, ".claude"+string(filepath.Separator)) {
			return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: symlink %s under .claude", rel)
		}
		return checkLink(root, rootR, path, rel)
	})
}

// checkLink refuses a symlink whose lexical target or resolved target
// leaves the tree. A dangling link is judged on its lexical target.
func checkLink(root, rootR, path, rel string) error {
	target, err := os.Readlink(path)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "claude: reading link %s", rel)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	if !within(root, filepath.Clean(target)) && !within(rootR, filepath.Clean(target)) {
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: symlink %s leaves the tree", rel)
	}
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil && !within(rootR, resolved) {
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: symlink %s resolves outside the tree", rel)
	}
	return nil
}

// within reports whether child is parent or lies under it.
func within(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// strictlyUnder reports whether child lies under parent and is not parent.
func strictlyUnder(parent, child string) bool {
	return filepath.Clean(parent) != filepath.Clean(child) && within(parent, child)
}

// refuseForeignSettings refuses any settings*.json file (any letter case)
// directly under claudeDir unless the hook pack at packSrc provides a
// regular file of the same name, which the copy then overwrites. A missing
// .claude/ holds nothing to refuse.
func refuseForeignSettings(claudeDir, packSrc string) error {
	entries, err := os.ReadDir(claudeDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "claude: reading the tree's .claude directory")
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if !strings.HasPrefix(name, "settings") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if packSrc != "" {
			if fi, err := os.Lstat(filepath.Join(packSrc, e.Name())); err == nil && fi.Mode().IsRegular() {
				continue
			}
		}
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrForeignHarnessSettings, "claude: .claude/%s is not the hook pack's", e.Name())
	}
	return nil
}

// copyHookPack copies every regular file under src into claudeDir (mode
// 0700 dirs, 0600 files) and returns the tree-relative paths it wrote. It
// refuses a symlink at every component it creates or writes: directories
// are checked with Lstat, files are opened with O_NOFOLLOW.
func copyHookPack(src, claudeDir string) ([]string, error) {
	if src == "" {
		return nil, nil
	}
	written := []string{}
	err := filepath.WalkDir(src, func(path string, de fs.DirEntry, werr error) error {
		if werr != nil {
			return cascade.Wrapf(cascade.KindInternal, werr, "claude: walking the hook pack")
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(claudeDir, rel)
		if de.IsDir() {
			return ensureRealDir(target)
		}
		if !de.Type().IsRegular() {
			return cascade.Newf(cascade.KindInvalidInput, "claude: hook pack entry %s is not a regular file", rel)
		}
		written = append(written, filepath.ToSlash(filepath.Join(".claude", rel)))
		return copyNoFollow(path, target)
	})
	return written, err
}

// ensureRealDir creates dir (0700) or accepts an existing real directory;
// an existing symlink or non-directory refuses.
func ensureRealDir(dir string) error {
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if mkErr := os.Mkdir(dir, 0o700); mkErr != nil {
			return cascade.Wrapf(cascade.KindInternal, mkErr, "claude: creating %s", dir)
		}
		return nil
	}
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: %s is not a real directory", dir)
	}
	return nil
}

func copyNoFollow(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "claude: opening hook pack file")
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|openNoFollow, 0o600)
	if err != nil {
		return cascade.Wrapf(cascade.KindCapabilityDenied, ErrWorktreeEscape, "claude: refusing to write %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return cascade.Wrapf(cascade.KindInternal, err, "claude: copying hook pack file")
	}
	return out.Close()
}
