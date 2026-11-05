//go:build !windows

package policy

// Purpose: the daemon-owned copy of the execution kernel, written outside
//   every worktree at <DataDir>/policy/policy-kernel.md so a spawn reads the
//   kernel from a path no repository can edit (P1-GEN-12).
// Inputs: the daemon's data directory (runtime.PathProvider.DataDir()).
// Outputs: the absolute path of the canonical kernel file.
// Constraints: the content is RenderKernelFile() and nothing else, so its
//   sha256 is provider.PolicyKernelSHA256. The policy directory is 0700 and
//   the file 0600. A symlink at the data directory or the policy directory
//   is refused, never followed: the daemon writes only under a path it owns.
//   A copy that differs (bytes or mode) is replaced atomically; a canonical
//   copy is left untouched. No clock, no environment. Unix only, like the
//   daemon that calls it (cmd/cascade/wire_policy_kernel.go).
// SPORT: context-engine/policy-kernel daemon file (ADD, P1-GEN-12a).

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// KernelFileRel is the daemon kernel file's path relative to the daemon's
// data directory, in slash form.
const KernelFileRel = "policy/policy-kernel.md"

// Modes of the daemon-owned policy directory and kernel file.
const (
	kernelDirMode  fs.FileMode = 0o700
	kernelFileMode fs.FileMode = 0o600
)

// EnsureDaemonKernelFile makes <dataDir>/policy/policy-kernel.md hold
// exactly RenderKernelFile() with mode 0600 and returns its path. It creates
// the policy directory 0700 when absent and refuses (KindConflict) when
// dataDir or the policy directory is a symlink or not a directory. A file
// that is absent or differs from the canonical bytes or mode is rewritten
// atomically. dataDir must be absolute (KindInvalidInput otherwise).
//
// It is distinct from EnsureKernelFile, which places the repository copy
// at provider.PolicyKernelFile and never overwrites it.
func EnsureDaemonKernelFile(dataDir string) (path string, err error) {
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return "", cascade.Newf(cascade.KindInvalidInput, "policy: data directory %q is not an absolute path", dataDir)
	}
	dataDir = filepath.Clean(dataDir)
	if err := os.MkdirAll(dataDir, kernelDirMode); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "policy: create %s", dataDir)
	}
	if _, err := requireRealDir(dataDir); err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, filepath.Dir(filepath.FromSlash(KernelFileRel)))
	if err := ensureOwnedDir(dir); err != nil {
		return "", err
	}
	path = filepath.Join(dataDir, filepath.FromSlash(KernelFileRel))
	want := RenderKernelFile()
	current, err := daemonKernelCurrent(path, want)
	if err != nil || current {
		return path, err
	}
	if err := runtime.WriteFileAtomic(path, want, kernelFileMode); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "policy: write %s", path)
	}
	return path, nil
}

// ensureOwnedDir creates dir 0700 when absent, refuses a symlink or
// non-directory there, and narrows a wider mode to 0700.
func ensureOwnedDir(dir string) error {
	if err := os.Mkdir(dir, kernelDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return cascade.Wrapf(cascade.KindUnavailable, err, "policy: create %s", dir)
	}
	info, err := requireRealDir(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != kernelDirMode {
		if err := os.Chmod(dir, kernelDirMode); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "policy: chmod %s", dir)
		}
	}
	return nil
}

// requireRealDir refuses p unless its own entry is a directory: a symlink
// (to a directory or not) is KindConflict and is never followed. It returns
// the entry's Lstat info.
func requireRealDir(p string) (fs.FileInfo, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "policy: stat %s", p)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, cascade.Newf(cascade.KindConflict, "policy: %s is a symlink; the daemon kernel directory must be a real directory", p)
	}
	if !info.IsDir() {
		return nil, cascade.Newf(cascade.KindConflict, "policy: %s is not a directory", p)
	}
	return info, nil
}

// daemonKernelCurrent reports whether a regular file at p already holds
// want with mode 0600. Absent, a symlink, or any difference is false; a
// directory at p is KindConflict.
func daemonKernelCurrent(p string, want []byte) (bool, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "policy: stat %s", p)
	}
	if info.IsDir() {
		return false, cascade.Newf(cascade.KindConflict, "policy: %s is a directory, not the kernel file", p)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != kernelFileMode || info.Size() != int64(len(want)) {
		return false, nil
	}
	got, err := os.ReadFile(p)
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "policy: read %s", p)
	}
	return bytes.Equal(got, want), nil
}
