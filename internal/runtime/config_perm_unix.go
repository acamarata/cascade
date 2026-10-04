//go:build !windows

package runtime

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Purpose: the unix half of the config.toml permission classifier shared
//   by Load and CheckConfigPermissions (see config_perm.go).
// Inputs: the opened config file and its target (path as given, resolved
//   path); fstat of the descriptor, lstat of the resolved name, stat of the
//   resolved file's parent directory and, for a symlinked path, of the
//   directory holding each link in the chain.
// Outputs: a ConfigPermFinding.
// Constraints: mode and owner come from the open descriptor, and the
//   resolved name must still be that file, so the parent directory checked
//   is the one holding the bytes Load reads. Only the immediate parent of
//   the resolved file and of each link is examined: another user who can
//   write a link's directory can retarget it. Each link's own owner (lstat)
//   must be you or root, since in a sticky directory only a link's owner can
//   replace it. A chain longer than maxConfigLinkHops is refused. uid 0 is
//   accepted as an owner.
//   Read-only: never changes the file.
// SPORT: runtime/config (ADD, P1-CORE-16).

func classifyOpenConfig(f *os.File, t configTarget) (ConfigPermFinding, error) {
	fi, err := f.Stat()
	if err != nil {
		return ConfigPermFinding{}, fmt.Errorf("runtime: stat config %s: %w", t.path, err)
	}
	name, fix := t.name(), t.fixFile()
	mode := uint32(fi.Mode().Perm())
	if mode&0o002 != 0 {
		return refuseFinding("%s is world-writable (mode %s); fix: chmod 600 %s", name, permMode(mode), fix), nil
	}
	if uid, ok := ownerUIDLookup(fi); ok && !trustedOwner(uid) {
		return refuseFinding("%s is owned by uid %d, not you or root; fix: chown $(id -u) %s", name, uid, fix), nil
	}
	// The resolved name must still hold the opened file, so the directories
	// judged next are the ones that hold the bytes Load reads.
	if li, err := os.Lstat(t.real); err != nil || !os.SameFile(fi, li) {
		return refuseFinding("%s changed while it was being checked; retry", name), nil
	}
	if pf, err := classifyParentDirs(t); err != nil || pf.Level == ConfigPermRefuse {
		return pf, err
	}
	return classifyFileMode(name, fix, mode), nil
}

// maxConfigLinkHops bounds the symlink chain walked from a config path
// (Linux itself follows at most 40); a longer chain is refused.
const maxConfigLinkHops = 40

// classifyParentDirs judges every directory another user could use to swap
// or redirect the config: the resolved file's parent and, when the path is
// a symlink, the directory holding each link of the chain. A link owned by
// another user refuses the path before any directory is judged.
func classifyParentDirs(t configTarget) (ConfigPermFinding, error) {
	dirs := []string{filepath.Dir(t.real)}
	if t.linked {
		hops, lf, err := linkHopDirs(t)
		if err != nil || lf.Level == ConfigPermRefuse {
			return lf, err
		}
		dirs = append(dirs, hops...)
	}
	for _, dir := range dirs {
		if pf, err := classifyParentDir(t, dir); err != nil || pf.Level == ConfigPermRefuse {
			return pf, err
		}
	}
	return ConfigPermFinding{Level: ConfigPermOK}, nil
}

// linkHopDirs walks the symlink chain from t.path with Readlink and returns
// the resolved directory holding every name on it (each link and the name
// it ends at). The finding refuses a link owned by another user (its own
// lstat uid, through the owner seam) or a chain of more than
// maxConfigLinkHops links.
func linkHopDirs(t configTarget) ([]string, ConfigPermFinding, error) {
	var dirs []string
	for cur, links := t.path, 0; ; links++ {
		dir, err := filepath.EvalSymlinks(filepath.Dir(cur))
		if err != nil {
			return nil, ConfigPermFinding{}, fmt.Errorf("runtime: resolve config link directory %s: %w", filepath.Dir(cur), err)
		}
		dirs = append(dirs, dir)
		li, err := os.Lstat(cur)
		if err != nil {
			return nil, ConfigPermFinding{}, fmt.Errorf("runtime: stat config link %s: %w", cur, err)
		}
		if li.Mode()&os.ModeSymlink == 0 {
			return dirs, ConfigPermFinding{Level: ConfigPermOK}, nil
		}
		if uid, ok := ownerUIDLookup(li); ok && !trustedOwner(uid) {
			return nil, refuseFinding("%s is reached through symlink %s owned by uid %d, not you or root; fix: chown -h $(id -u) %s", t.name(), cur, uid, cur), nil
		}
		if links == maxConfigLinkHops {
			return nil, refuseFinding("%s is a chain of more than %d symlinks; fix: link it to the file directly", t.name(), maxConfigLinkHops), nil
		}
		next, err := os.Readlink(cur)
		if err != nil {
			return nil, ConfigPermFinding{}, fmt.Errorf("runtime: read config link %s: %w", cur, err)
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(dir, next)
		}
		cur = next
	}
}

// classifyParentDir refuses a directory holding the resolved file or one
// of its links that another user could use to swap it: world-writable
// without the sticky bit, or foreign-owned.
func classifyParentDir(t configTarget, dir string) (ConfigPermFinding, error) {
	di, err := os.Stat(dir)
	if err != nil {
		return ConfigPermFinding{}, fmt.Errorf("runtime: stat config directory %s: %w", dir, err)
	}
	dmode := uint32(di.Mode().Perm())
	if dmode&0o002 != 0 && di.Mode()&os.ModeSticky == 0 {
		return refuseFinding("%s sits in world-writable directory %s (mode %s); fix: chmod o-w %s", t.name(), dir, permMode(dmode), dir), nil
	}
	if uid, ok := ownerUIDLookup(di); ok && !trustedOwner(uid) {
		return refuseFinding("%s sits in directory %s owned by uid %d, not you or root; fix: chown $(id -u) %s", t.name(), dir, uid, dir), nil
	}
	return ConfigPermFinding{Level: ConfigPermOK}, nil
}

// classifyFileMode handles the non-refusal cases by the file's own mode.
func classifyFileMode(name, fix string, mode uint32) ConfigPermFinding {
	switch {
	case mode&0o020 != 0:
		return ConfigPermFinding{Level: ConfigPermWarn, loadWarns: true, Reason: fmt.Sprintf(
			"%s is group-writable (mode %s); a shared config is allowed but reviewable; fix: chmod 600 %s", name, permMode(mode), fix)}
	case mode&0o004 != 0:
		return ConfigPermFinding{Level: ConfigPermWarn, Reason: fmt.Sprintf(
			"%s is world-readable (mode %s); fix: chmod 600 %s", name, permMode(mode), fix)}
	}
	return ConfigPermFinding{Level: ConfigPermOK}
}

func refuseFinding(format string, args ...interface{}) ConfigPermFinding {
	return ConfigPermFinding{Level: ConfigPermRefuse, Reason: fmt.Sprintf(format, args...)}
}

// ownerUIDLookup is the owner seam both classifiers call. Only tests set
// it (export_test.go), so foreign-owner refusals run without root;
// TestOwnerSeamHasNoProductionSetter keeps production code from setting it.
var ownerUIDLookup = ownerUID

// ownerUID reads the owning uid from the file's Stat_t.
func ownerUID(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

func trustedOwner(uid int) bool { return uid == 0 || uid == os.Geteuid() }

// permMode renders a permission mask as 0NNN for messages.
func permMode(m uint32) string { return fmt.Sprintf("%04o", m) }
