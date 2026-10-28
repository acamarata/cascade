package uninstall

// Purpose: turn the install receipt into an executable plan: which files
//   in which directory the receipt proves Cascade installed, or why the
//   receipt is refused.
// Inputs: Deps.Receipt, Deps.Executable, Deps.GOOS.
// Outputs: execPlan and fileStatus.
// Constraints: fail closed. install_dir must be absolute, a real directory
//   (Lstat, not a symlink) and the directory of the resolved running
//   executable; every name must be a bare member of the fixed executable
//   set, listed once, with a sha256 digest. Any violation refuses every
//   executable item (KindIntegrity). A file is examined through an
//   os.Root at install_dir, so no name or symlink reaches outside it.
// SPORT: internal/uninstall receipt plan/ADDED.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/update"
	"github.com/acamarata/cascade/pkg/cascade"
)

// executableBases is the fixed set install.sh installs, in removal order:
// helper and plugin before cascade itself.
var executableBases = []string{"cascade-elevate-helper", "cascade-github", "cascade"}

// receiptFileName is the receipt install.sh writes under the cascade root.
const receiptFileName = "install-receipt.json"

// execPlan is what the receipt proves.
type execPlan struct {
	present    bool           // a receipt exists
	refused    error          // non-nil: every executable item refuses
	receipt    update.Receipt // valid only when present and not refused
	running    string         // the resolved running executable, "" if unknown
	goos       string
	installDir string
	resolved   string // install_dir through symlinks, set when the plan is usable
}

// fileState is one receipt file's on-disk state.
type fileState int

const (
	fileAbsent   fileState = iota // nothing at the path
	fileMatches                   // a regular file whose sha256 equals the receipt's
	fileModified                  // a regular file with another digest
	fileRefused                   // a symlink or non-regular file
)

// buildExecPlan reads and validates the receipt.
func buildExecPlan(d Deps) execPlan {
	p := execPlan{goos: d.GOOS, running: runningExecutable(d)}
	if d.Receipt == nil {
		return p
	}
	r, ok, err := d.Receipt()
	switch {
	case err != nil:
		p.present = true
		p.refused = cascade.Wrap(cascade.KindIntegrity, err, "uninstall: the install receipt is refused")
		return p
	case !ok:
		return p
	}
	p.present, p.receipt, p.installDir = true, r, r.InstallDir
	p.refused = validateReceipt(r, p.running, d.GOOS)
	if p.refused == nil {
		p.resolved, _ = filepath.EvalSymlinks(r.InstallDir)
	}
	return p
}

// runningExecutable is the symlink-resolved running binary, or "".
func runningExecutable(d Deps) string {
	if d.Executable == nil {
		return ""
	}
	exe, err := d.Executable()
	if err != nil || exe == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		return r
	}
	return ""
}

// validateReceipt applies every refusal rule; nil means the plan is usable.
func validateReceipt(r update.Receipt, running, goos string) error {
	refuse := func(format string, args ...any) error {
		return cascade.Newf(cascade.KindIntegrity, "uninstall: install receipt refused: "+format, args...)
	}
	dir := r.InstallDir
	if !filepath.IsAbs(dir) {
		return refuse("install_dir %q is not absolute", dir)
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return refuse("install_dir %s is not a real directory", dir)
	}
	if running == "" {
		return refuse("the running executable could not be resolved to compare with install_dir %s", dir)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || filepath.Dir(running) != resolved {
		return refuse("install_dir %s is not the running executable's directory %s", dir, filepath.Dir(running))
	}
	seen := map[string]bool{}
	for _, f := range r.Files {
		if !fixedName(f.Name, goos) || seen[f.Name] || !hexDigest(f.SHA256) {
			return refuse("file entry %q is not a unique cascade executable with a sha256 digest", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}

// fixedName reports whether name is a bare member of the executable set.
func fixedName(name, goos string) bool {
	if strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return false
	}
	for _, b := range executableBases {
		if name == exeName(b, goos) {
			return true
		}
	}
	return false
}

// exeName adds ".exe" on windows.
func exeName(base, goos string) string {
	if goos == "windows" {
		return base + ".exe"
	}
	return base
}

// hexDigest reports a lower-case sha256 hex digest.
func hexDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// fileStatus examines name under the plan's install_dir without following
// a symlink at the name.
func (p execPlan) fileStatus(name, want string) (fileState, error) {
	root, err := os.OpenRoot(p.installDir)
	if err != nil {
		return fileRefused, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: open %s", p.installDir)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return fileAbsent, nil
	}
	if err != nil {
		return fileRefused, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: stat %s", name)
	}
	if !info.Mode().IsRegular() {
		return fileRefused, nil
	}
	f, err := root.Open(name)
	if err != nil {
		return fileRefused, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: read %s", name)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fileRefused, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: hash %s", name)
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fileModified, nil
	}
	return fileMatches, nil
}
