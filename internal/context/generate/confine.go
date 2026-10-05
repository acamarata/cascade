package generate

// Purpose: resolve every path the package touches under the repository
//   root and refuse anything that leaves it. Nothing generated may ever be
//   written through a symlinked .claude, .cascade or .codex into an
//   owner's global config.
// Inputs: the repository root and a slash-separated repo-relative path.
// Outputs: a confined handle that reads and prepares the path, or a
//   KindInvalidInput error wrapping ErrPathEscape.
// Constraints: the root is opened with os.Root, which resolves each
//   component relative to the previous directory descriptor and refuses
//   escapes (openat with O_NOFOLLOW on unix, reparse-point checks on
//   windows). On top of that every existing component is Lstat'd and a
//   symlink or reparse point is refused, even one that stays inside the
//   root, and EvalSymlinks(parent) must stay under EvalSymlinks(root).
//   Writes still go through runtime.WriteFileAtomic by path, so a same-uid
//   process that swaps a directory for a symlink between the check and the
//   rename can redirect that one write; see the residual-window note in
//   merge.go.
// SPORT: context-engine/path-confinement (ADD, P1-GEN-05).

import (
	"errors"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrPathEscape is the sentinel of every confinement refusal. Compare it
// by identity, never with errors.Is (which compares Kind only).
var ErrPathEscape = cascade.New(cascade.KindInvalidInput, "generate: path-escape")

// escapeErr builds a confinement refusal for rel.
func escapeErr(rel, why string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrPathEscape, "generate: path-escape: %s: %s", rel, why)
}

// isPathEscape reports whether err is a confinement refusal.
func isPathEscape(err error) bool { return hasSentinel(err, ErrPathEscape) }

// hasSentinel walks err's chain and reports a *cascade.Error whose wrapped
// error is exactly sentinel (identity, not Kind).
func hasSentinel(err error, sentinel *cascade.Error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if ce, ok := err.(*cascade.Error); ok && ce.Err == sentinel {
			return true
		}
	}
	return false
}

// confined is one repo-relative path resolved under an opened root.
type confined struct {
	root   *os.Root
	rootAt string
	rel    string
	abs    string
	exists bool
	mode   os.FileMode
}

// checkRel refuses any path that is not a plain relative slash path.
func checkRel(rel string) error {
	if rel == "" || rel == "." || strings.ContainsAny(rel, "\\\x00") || !fs.ValidPath(rel) ||
		!filepath.IsLocal(filepath.FromSlash(rel)) {
		return escapeErr(rel, "not a plain repository-relative path")
	}
	return nil
}

// openConfined opens repoRoot and resolves rel beneath it. The caller
// closes the handle. Nothing is created.
func openConfined(repoRoot, rel string) (*confined, error) {
	if repoRoot == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "generate: empty repository root")
	}
	if err := checkRel(rel); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		kind := cascade.KindUnavailable
		if errors.Is(err, fs.ErrNotExist) {
			kind = cascade.KindNotFound
		}
		return nil, cascade.Wrapf(kind, err, "generate: open repository root %s", repoRoot)
	}
	abs, err := filepath.Abs(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		_ = root.Close()
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "generate: resolve %s", rel)
	}
	c := &confined{root: root, rootAt: repoRoot, rel: rel, abs: abs}
	if err := c.walk(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return c, nil
}

// close releases the root handle.
func (c *confined) close() { _ = c.root.Close() }

// walk Lstats each component from the root down. A missing component ends
// the walk (nothing below it exists). A symlink or reparse point anywhere,
// a non-directory parent or a non-regular target is refused.
func (c *confined) walk() error {
	c.exists, c.mode = false, 0
	parts := strings.Split(c.rel, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		fi, err := c.root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return escapeErr(prefix, err.Error())
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return escapeErr(prefix, "symlink or reparse point")
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return cascade.Newf(cascade.KindInvalidInput, "generate: %s is not a directory", prefix)
		}
		if i == len(parts)-1 {
			if !fi.Mode().IsRegular() {
				return cascade.Newf(cascade.KindInvalidInput, "generate: %s is not a regular file", prefix)
			}
			c.exists, c.mode = true, fi.Mode()
			return c.parentInsideRoot()
		}
	}
	return nil
}

// parentInsideRoot is the second check: the resolved parent directory must
// sit under the resolved root.
func (c *confined) parentInsideRoot() error {
	evalRoot, err := filepath.EvalSymlinks(c.rootAt)
	if err != nil {
		return escapeErr(c.rel, "root does not resolve")
	}
	evalParent, err := filepath.EvalSymlinks(filepath.Dir(c.abs))
	if err != nil {
		return escapeErr(c.rel, "parent does not resolve")
	}
	rel, err := filepath.Rel(evalRoot, evalParent)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return escapeErr(c.rel, "resolved parent is outside the repository root")
	}
	return nil
}

// read returns the target's bytes. A target that is absent reports
// (nil, false, nil).
func (c *confined) read() ([]byte, bool, error) {
	if !c.exists {
		return nil, false, nil
	}
	data, err := c.root.ReadFile(c.rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, cascade.Wrapf(cascade.KindUnavailable, err, "generate: read %s", c.rel)
	}
	return data, true, nil
}

// ensureParent creates the missing parent directories (mode 0755) beneath
// the root. It runs only after walk accepted the path.
func (c *confined) ensureParent() error {
	dir := pathpkg.Dir(c.rel)
	if dir == "." {
		return nil
	}
	if err := c.root.MkdirAll(dir, 0o755); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "generate: create directory %s", dir)
	}
	return nil
}
