// Package plugins (process_exec.go): Purpose: locates a first-party
// process plugin's executable and pins its digest for the trust grant.
// Inputs: a plugin id; the directory of the running cascade binary.
// Outputs: the executable's path and its lowercase sha256 hex digest.
// Constraints: the executable is <dir>/<id> (".exe" on windows) where dir is
// the directory of the resolved cascade binary. There is no PATH lookup and
// no environment override; only a test binary may inject the directory
// (SetExecutableDirForTest). The file must be a regular file, not a symlink,
// and on unix not group- or world-writable; anything else is refused.
// SPORT: internal/plugins process executable resolution (P1-PLG-10).
package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// errExecutableRefused opens every refusal ResolveExecutable and
// ExecutableDigest give for an executable that cannot be trusted; each one
// then names its specific reason.
const errExecutableRefused = "process plugin executable is missing, not a regular file, or group/world-writable"

var (
	execDirMu       sync.RWMutex
	execDirOverride string
)

// SetExecutableDirForTest makes ResolveExecutable look in dir instead of the
// cascade binary's directory until restore is called. It panics outside a
// test binary, so no production caller can redirect the lookup.
func SetExecutableDirForTest(dir string) (restore func()) {
	if !testing.Testing() {
		panic("plugins: SetExecutableDirForTest outside a test binary")
	}
	execDirMu.Lock()
	previous := execDirOverride
	execDirOverride = dir
	execDirMu.Unlock()
	return func() {
		execDirMu.Lock()
		execDirOverride = previous
		execDirMu.Unlock()
	}
}

// executableDir is the directory of the resolved running binary, or the
// test override.
func executableDir() (string, error) {
	execDirMu.RLock()
	dir := execDirOverride
	execDirMu.RUnlock()
	if dir != "" {
		return dir, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}
	return filepath.Dir(resolved), nil
}

// The specific reasons a refusal names after errExecutableRefused.
const (
	reasonMissing    = "cannot be read"
	reasonIrregular  = "not a regular file"
	reasonWritable   = "writable by group or others"
	reasonSwapped    = "changed while it was read"
	reasonUnresolved = "the cascade binary's directory cannot be resolved"
)

// refuseExecutable builds the KindUnavailable refusal for pluginID, naming
// the specific reason; cause (may be nil) is wrapped.
func refuseExecutable(pluginID, reason string, cause error) error {
	if cause == nil {
		return cascade.Newf(cascade.KindUnavailable, "plugin: %s: %s (%s)", pluginID, errExecutableRefused, reason)
	}
	return cascade.Wrapf(cascade.KindUnavailable, cause, "plugin: %s: %s (%s)", pluginID, errExecutableRefused, reason)
}

// untrustable returns why info is not a file the grant may pin, or "": it
// must be a regular file (a symlink is not one) that, on unix, neither the
// group nor others can write.
func untrustable(info os.FileInfo) string {
	if !info.Mode().IsRegular() {
		return reasonIrregular
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return reasonWritable
	}
	return ""
}

// processPluginID is the manifest id shape, the same pattern as
// pkg/plugin's idPatternRe (Validate's id rule): an id outside it never
// names a file here (TestResolveExecutableIDMatchesManifestRule).
var processPluginID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// beforeDigestOpen, when set by a test, runs between ExecutableDigest's
// Lstat and its Open, so a test can swap the file there.
var beforeDigestOpen func(path string)

// ResolveExecutable returns the path of pluginID's first-party executable
// beside the cascade binary. A malformed id is KindInvalidInput; a missing
// file, a symlink, a non-regular file or, on unix, a group- or world-writable
// one is KindUnavailable. Windows has no writability check.
func ResolveExecutable(pluginID string) (string, error) {
	if !processPluginID.MatchString(pluginID) {
		return "", cascade.Newf(cascade.KindInvalidInput, "plugin: %q is not a valid process plugin id", pluginID)
	}
	dir, err := executableDir()
	if err != nil {
		return "", refuseExecutable(pluginID, reasonUnresolved, err)
	}
	name := pluginID
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", refuseExecutable(pluginID, reasonMissing, err)
	}
	if reason := untrustable(info); reason != "" {
		return "", refuseExecutable(pluginID, reason, nil)
	}
	return path, nil
}

// ExecutableDigest returns the lowercase sha256 hex of the file at path. It
// re-checks the opened file (still regular, still not group- or
// world-writable, and the same file the path names now), so a swap between
// ResolveExecutable and this read is refused rather than pinned.
func ExecutableDigest(path string) (string, error) {
	id := filepath.Base(path)
	before, err := os.Lstat(path)
	if err != nil {
		return "", refuseExecutable(id, reasonMissing, err)
	}
	if reason := untrustable(before); reason != "" {
		return "", refuseExecutable(id, reason, nil)
	}
	if beforeDigestOpen != nil {
		beforeDigestOpen(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", refuseExecutable(id, reasonMissing, err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || untrustable(opened) != "" || !os.SameFile(before, opened) {
		return "", refuseExecutable(id, reasonSwapped, err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", refuseExecutable(id, reasonMissing, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
