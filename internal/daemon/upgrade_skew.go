//go:build !windows

// Package daemon implements the long-lived cascade daemon. This file holds
// the running binary's startup identity and the skew check built on it.
//
// The build identity is the content digest of the file the daemon was
// started from, captured once before the listener accepts. No linker stamp
// takes part: a binary cannot embed its own digest, and a version string or
// git SHA can never equal a file digest. Skew means the installed file no
// longer has the bytes the daemon started with.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/cascade"
)

// unstampedBuildHash is BuildHash's answer when the running executable
// cannot be resolved or hashed. wireUpgrade declines to wire the manager on
// it, and CheckSkew never reports skew against it.
const unstampedBuildHash = "dev"

// startupIdentity is the running binary as it was at start: the path the OS
// reported (symlinks unresolved), that path with every symlink resolved, and
// the sha256 of the resolved file. load captures it at most once.
type startupIdentity struct {
	resolve     func() (string, error)
	once        sync.Once
	installPath string
	path        string
	digest      string
	err         error
}

// startup holds the process's identity. Tests in this package swap it for
// one with a different resolver; nothing outside the package can, so no
// exported test seam exists.
var startup atomic.Pointer[startupIdentity]

func init() { startup.Store(newStartupIdentity(os.Executable)) }

func newStartupIdentity(resolve func() (string, error)) *startupIdentity {
	return &startupIdentity{resolve: resolve}
}

// load captures the identity on first use and returns it. The digest is
// read from the file once; later calls never reopen it.
func (s *startupIdentity) load() *startupIdentity {
	s.once.Do(func() {
		s.installPath, s.path, s.digest, s.err = captureStartup(s.resolve)
	})
	return s
}

// captureStartup resolves the executable, resolves its symlinks and hashes
// the file they name. Any failure is returned, never an empty success.
func captureStartup(resolve func() (string, error)) (install, path, digest string, err error) {
	if resolve == nil {
		return "", "", "", errors.New("no executable resolver")
	}
	if install, err = resolve(); err != nil {
		return "", "", "", err
	}
	if path, err = filepath.EvalSymlinks(install); err != nil {
		return "", "", "", err
	}
	if digest, err = hashFile(path); err != nil {
		return "", "", "", err
	}
	return install, path, digest, nil
}

// BuildHash returns the hex sha256 of the file this process was started
// from, captured on the first call (Run makes that call before its listener
// accepts). If the executable cannot be resolved or read it returns the
// "dev" sentinel, the value wireUpgrade's guard already declines.
func BuildHash() string {
	id := startup.Load().load()
	if id.err != nil {
		return unstampedBuildHash
	}
	return id.digest
}

// BuildHash returns the package-level BuildHash, for holders of a manager.
func (m *UpgradeManager) BuildHash() string { return BuildHash() }

// StartupPath returns the resolved path BuildHash hashed, or "" when the
// startup identity could not be captured.
func (m *UpgradeManager) StartupPath() string {
	id := startup.Load().load()
	if id.err != nil {
		return ""
	}
	return id.path
}

// CheckSkew reports whether the installed binary differs from the bytes this
// daemon started with. It fails closed: when either side cannot be read it
// returns false with a typed cascade.KindUnavailable error, never a quiet
// "no skew", and callers must not treat that error as "unchanged".
func (m *UpgradeManager) CheckSkew() (bool, error) {
	skew, _, _, _, err := m.checkSkew()
	return skew, err
}

// checkSkew also returns the file it hashed, its sum and the install path. The
// install path is resolved again here, so a symlinked install re-pointed at
// a different file is compared by that file's content. An install path that
// is not a symlink resolves to StartupPath, which is then rehashed.
func (m *UpgradeManager) checkSkew() (bool, string, string, string, error) {
	id := startup.Load().load()
	if id.err != nil {
		return false, "", "", "", cascade.Wrap(cascade.KindUnavailable, id.err, "daemon: upgrade: startup digest unavailable")
	}
	if id.digest == "" || id.digest == unstampedBuildHash {
		return false, "", "", "", cascade.New(cascade.KindUnavailable, "daemon: upgrade: startup digest unavailable")
	}
	target, err := filepath.EvalSymlinks(id.installPath)
	if err != nil {
		return false, "", "", "", cascade.Wrapf(cascade.KindUnavailable, err, "daemon: upgrade: resolve %s", id.installPath)
	}
	sum, err := hashFile(target)
	if err != nil {
		return false, "", "", "", cascade.Wrapf(cascade.KindUnavailable, err, "daemon: upgrade: hash %s", target)
	}
	return sum != id.digest, target, sum, id.installPath, nil
}

// relaunchPath retains a symlinked install across exec while it still names
// the verified target; otherwise the resolved, verified target wins.
func relaunchPath(installPath, target string) string {
	if resolved, err := filepath.EvalSymlinks(installPath); err == nil && resolved == target {
		return installPath
	}
	return target
}

// hashFile streams path through SHA-256 rather than loading it whole.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
