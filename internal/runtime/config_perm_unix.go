//go:build !windows

package runtime

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Purpose: the unix half of the config.toml permission classifier shared
//   by Load and CheckConfigPermissions (see config_perm.go).
// Inputs: the opened config file and its target (path as given, resolved
//   path); fstat of the descriptor, lstat of the resolved name, and a walk
//   of the given path one component at a time (Lstat and Readlink) that
//   stats every directory it judges.
// Outputs: a ConfigPermFinding.
// Constraints: mode and owner come from the open descriptor, and the
//   resolved name must still be that file, so the directories checked are
//   the ones holding the bytes Load reads. Every directory from the
//   resolved file's parent up to "/", and the directory holding each
//   symlink component (directory component or final name) with all its
//   ancestors, must be neither group- nor other-writable unless it is
//   root-owned and sticky (the /tmp case), and must be owned by you or root
//   (sshd StrictModes style). Each link's owner (lstat) must be you or
//   root, since in a sticky directory only a link's owner can replace it;
//   link-owner refusals precede directory judgements. The final-name chain
//   is bounded by maxConfigLinkHops, directory-component links separately
//   by maxConfigDirLinkHops. uid 0 is accepted as an owner.
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

// maxConfigLinkHops bounds the final-name symlink chain of a config path
// (Linux itself follows at most 40); a longer chain is refused.
const maxConfigLinkHops = 40

// maxConfigDirLinkHops bounds the directory-component links met on the way
// (the filepath.EvalSymlinks limit), counted apart from the final-name chain
// so a system link such as /tmp -> /private/tmp never uses that budget.
const maxConfigDirLinkHops = 255

// classifyParentDirs judges the ancestors of the resolved file and of
// every component link. Link-owner refusals precede directory judgements.
func classifyParentDirs(t configTarget) (ConfigPermFinding, error) {
	dirs, lf, err := linkHopDirs(t)
	if err != nil || lf.Level == ConfigPermRefuse {
		return lf, err
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		for !seen[dir] {
			seen[dir] = true
			if pf, err := classifyParentDir(t, dir); err != nil || pf.Level == ConfigPermRefuse {
				return pf, err
			}
			dir = filepath.Dir(dir)
		}
	}
	return ConfigPermFinding{Level: ConfigPermOK}, nil
}

// linkHopDirs resolves the given path one component at a time, so a later
// dot-dot applies to the resolved directory, never to the link's spelling.
// It returns the resolved file's parent, then each link-holding directory,
// and refuses when the walk does not end at t.real.
func linkHopDirs(t configTarget) ([]string, ConfigPermFinding, error) {
	path := t.path
	if !filepath.IsAbs(path) {
		// Start from the cwd the kernel opens from, not $PWD's spelling.
		cwd, err := physicalPath(".")
		if err != nil {
			return nil, ConfigPermFinding{}, err
		}
		path = cwd + string(os.PathSeparator) + path
	}
	w := &linkWalk{t: t, cur: string(os.PathSeparator), pending: strings.Split(path, string(os.PathSeparator))}
	for len(w.pending) > 0 {
		if f, err := w.step(); err != nil || f.Level == ConfigPermRefuse {
			return nil, f, err
		}
	}
	want, err := physicalPath(t.real)
	if err != nil || w.cur != want {
		return nil, refuseFinding("%s changed while it was being checked; retry", t.name()), nil
	}
	return append([]string{filepath.Dir(w.cur)}, w.dirs...), ConfigPermFinding{Level: ConfigPermOK}, nil
}

// physicalPath resolves p to an absolute path without links. A relative p
// is joined to the working directory the kernel reports (getcwd(2), never
// $PWD, whose link can be re-pointed between reading and resolving it),
// the one the kernel opens p from, so a ".." in p never climbs the
// spelling of a linked cwd.
func physicalPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		cwd, err := syscall.Getwd()
		if err == nil {
			cwd, err = filepath.EvalSymlinks(cwd)
		}
		if err != nil {
			return "", fmt.Errorf("runtime: read working directory for config %s: %w", p, err)
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.EvalSymlinks(p)
}

// linkWalk is linkHopDirs' state: cur is the resolved prefix so far,
// pending the components still to resolve, dirs the link-holding
// directories, links and dirLinks the final-name and directory-component
// link counts.
type linkWalk struct {
	t               configTarget
	cur             string
	pending, dirs   []string
	links, dirLinks int
}

// step resolves the next pending component against the resolved prefix.
func (w *linkWalk) step() (ConfigPermFinding, error) {
	part := w.pending[0]
	w.pending = w.pending[1:]
	switch part {
	case "", ".":
		return ConfigPermFinding{Level: ConfigPermOK}, nil
	case "..":
		w.cur = filepath.Dir(w.cur)
		return ConfigPermFinding{Level: ConfigPermOK}, nil
	}
	dir := w.cur
	w.cur = filepath.Join(w.cur, part)
	li, err := os.Lstat(w.cur)
	if err != nil {
		return ConfigPermFinding{}, fmt.Errorf("runtime: stat config link %s: %w", w.cur, err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		return ConfigPermFinding{Level: ConfigPermOK}, nil
	}
	return w.follow(dir, li)
}

// follow records the link at w.cur held by dir, judges its owner and the
// hop bounds, and queues its target's components in place of the link.
func (w *linkWalk) follow(dir string, li fs.FileInfo) (ConfigPermFinding, error) {
	w.dirs = append(w.dirs, dir)
	final := len(w.pending) == 0
	if uid, ok := ownerUIDLookup(li); ok && !trustedOwner(uid) {
		// The given path names the first final-name link as the operator wrote it.
		shown := w.cur
		if final && w.links == 0 {
			shown = w.t.path
		}
		return refuseFinding("%s is reached through symlink %s owned by uid %d, not you or root; fix: chown -h $(id -u) %s", w.t.name(), shown, uid, shown), nil
	}
	if final {
		w.links++
	} else {
		w.dirLinks++
	}
	switch {
	case w.links > maxConfigLinkHops:
		return refuseFinding("%s is a chain of more than %d symlinks; fix: link it to the file directly", w.t.name(), maxConfigLinkHops), nil
	case w.dirLinks > maxConfigDirLinkHops:
		return refuseFinding("%s is reached through more than %d directory symlinks; fix: use a path with fewer links", w.t.name(), maxConfigDirLinkHops), nil
	}
	next, err := os.Readlink(w.cur)
	if err != nil {
		return ConfigPermFinding{}, fmt.Errorf("runtime: read config link %s: %w", w.cur, err)
	}
	w.cur = dir
	if filepath.IsAbs(next) {
		w.cur = string(os.PathSeparator)
	}
	w.pending = append(strings.Split(next, string(os.PathSeparator)), w.pending...)
	return ConfigPermFinding{Level: ConfigPermOK}, nil
}

// classifyParentDir refuses writable or foreign-owned ancestors. Only a
// root-owned sticky directory is exempt from the writable-mode refusal.
// Within one directory the mode is judged before the owner.
func classifyParentDir(t configTarget, dir string) (ConfigPermFinding, error) {
	di, err := os.Stat(dir)
	if err != nil {
		return ConfigPermFinding{}, fmt.Errorf("runtime: stat config directory %s: %w", dir, err)
	}
	dmode := uint32(di.Mode().Perm())
	uid, known := ownerUIDLookup(di)
	rootSticky := di.Mode()&os.ModeSticky != 0 && known && uid == 0
	if dmode&0o022 != 0 && !rootSticky {
		kind, fix := "group-writable", "chmod g-w "+dir
		switch {
		case dmode&0o022 == 0o022:
			kind, fix = "world-writable", "chmod o-w "+dir+" && "+fix
		case dmode&0o002 != 0:
			kind, fix = "world-writable", "chmod o-w "+dir
		}
		return refuseFinding("%s sits in %s directory %s (mode %s); fix: %s", t.name(), kind, dir, permMode(dmode), fix), nil
	}
	if known && !trustedOwner(uid) {
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
