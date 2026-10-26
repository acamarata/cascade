package update

// Purpose: install a verified archive's executables with a way back: stage
//   each beside its target, keep the current set as the one previous
//   generation, swap helper and plugin first and cascade last, and restore
//   on any failure.
// Inputs: a VerifiedArchive, the receipt's install dir, the previous dir
//   (<dataDir>/update/previous).
// Outputs: staged paths, a SwapRecord, restored files.
// Constraints: staged files are <dir>/.<name>.new, fsynced, mode 0755
//   (runtime.WriteReaderAtomic). The previous dir is reset, then filled
//   with the current files and the previous receipt (receipt.json), all
//   fsynced, before the first rename; it holds exactly one generation. No
//   journal: an interrupted swap is refused by the install check and
//   repaired by install.sh. A rename failure restores everything already swapped from
//   the previous copies and removes files that did not exist before. A
//   symlink or directory at a target refuses before anything changes.
// SPORT: internal/update Stage/ADDED, Swap/ADDED, Restore/ADDED.

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// previousReceiptName is the previous generation's receipt in the previous dir.
const previousReceiptName = "receipt.json"

// renameFile is os.Rename; a package variable only so a test can inject a
// rename failure on one file.
var renameFile = os.Rename

// SwapRecord is what Restore needs to undo one Swap.
type SwapRecord struct {
	InstallDir, PreviousDir string
	// Files are the names swapped in, in swap order.
	Files []string
	// Previous is the receipt the install had before the swap.
	Previous Receipt
}

func unavailable(err error, format string, args ...any) error {
	return cascade.Wrapf(cascade.KindUnavailable, err, "update: "+format, args...)
}

// Stage copies each verified executable to installDir/.<name>.new.
func Stage(va VerifiedArchive, installDir string) ([]string, error) {
	var staged []string
	for _, name := range va.Executables {
		if err := checkTarget(installDir, name); err != nil {
			removeAll(staged)
			return nil, err
		}
		dst := filepath.Join(installDir, "."+name+".new")
		if err := copyFileAtomic(filepath.Join(va.Dir, name), dst); err != nil {
			removeAll(staged)
			return nil, err
		}
		staged = append(staged, dst)
	}
	return staged, nil
}

// checkTarget refuses a target that is a symlink or not a regular file.
func checkTarget(installDir, name string) error {
	info, err := os.Lstat(filepath.Join(installDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return integrity("%s is not a regular file; rerun install.sh", filepath.Join(installDir, name))
	}
	return nil
}

// Swap keeps the current files and previous (the receipt they were
// installed under, as previousDir/receipt.json) in previousDir, all fsynced
// before the first rename, then renames each staged file into place,
// cascade last. Any error restores and is returned. An interrupted swap
// leaves a mixed install that Check refuses ("install differs from
// receipt"); the previous set stays in previousDir for a rerun of install.sh.
func Swap(staged []string, installDir, previousDir string, previous Receipt) (SwapRecord, error) {
	names, err := stagedNames(staged, installDir)
	if err != nil {
		return SwapRecord{}, err
	}
	if err := keepPrevious(names, installDir, previousDir, previous); err != nil {
		removeAll(staged)
		return SwapRecord{}, err
	}
	rec := SwapRecord{InstallDir: installDir, PreviousDir: previousDir, Previous: previous}
	for _, name := range names {
		src := filepath.Join(installDir, "."+name+".new")
		if err := replaceFile(src, filepath.Join(installDir, name)); err != nil {
			removeAll(staged)
			swapErr := unavailable(err, "swap %s", name)
			if rerr := Restore(rec); rerr != nil {
				return SwapRecord{}, errors.Join(swapErr, restoreFailed(rerr))
			}
			return SwapRecord{}, swapErr
		}
		rec.Files = append(rec.Files, name)
	}
	return rec, nil
}

// restoreFailed is the refusal for a restore that did not complete: the
// install may be mixed, and install.sh is the way back.
func restoreFailed(rerr error) error {
	return cascade.Wrap(cascade.KindIntegrity, rerr, "update: restoring the previous files failed; rerun install.sh")
}

// stagedNames maps staged paths back to target names, in swap order.
func stagedNames(staged []string, installDir string) ([]string, error) {
	var names []string
	for _, p := range staged {
		base := filepath.Base(p)
		name := strings.TrimSuffix(strings.TrimPrefix(base, "."), ".new")
		if filepath.Dir(p) != filepath.Clean(installDir) || base != "."+name+".new" ||
			(!knownExecutable(name, "windows") && !knownExecutable(name, "linux")) {
			return nil, cascade.Newf(cascade.KindInvalidInput, "update: %s is not a staged executable in %s", p, installDir)
		}
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool { return swapRank(names[i]) < swapRank(names[j]) })
	return names, nil
}

// swapRank orders helper, plugin, then cascade.
func swapRank(name string) int {
	base := strings.TrimSuffix(name, ".exe")
	for i, b := range executableBases {
		if b == base {
			return i
		}
	}
	return len(executableBases)
}

// keepPrevious resets previousDir, copies every current target into it and
// writes the previous receipt beside them.
func keepPrevious(names []string, installDir, previousDir string, previous Receipt) error {
	if err := os.RemoveAll(previousDir); err != nil {
		return unavailable(err, "reset %s", previousDir)
	}
	if err := os.MkdirAll(previousDir, 0o700); err != nil {
		return unavailable(err, "create %s", previousDir)
	}
	for _, name := range names {
		cur := filepath.Join(installDir, name)
		if err := checkTarget(installDir, name); err != nil {
			return err
		}
		if _, err := os.Lstat(cur); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyFileAtomic(cur, filepath.Join(previousDir, name)); err != nil {
			return err
		}
	}
	return WriteReceipt(filepath.Join(previousDir, previousReceiptName), previous)
}

// Restore puts back every swapped file and removes the ones that did not
// exist before. It reports every failure. On unix the previous copy is
// renamed over the target; on windows the renamed-aside <name>.old (the
// image a running process may hold) is renamed back, never deleted or
// overwritten (replace_windows.go).
func Restore(r SwapRecord) error {
	var errs []error
	for i := len(r.Files) - 1; i >= 0; i-- {
		name := r.Files[i]
		target := filepath.Join(r.InstallDir, name)
		prev := filepath.Join(r.PreviousDir, name)
		if _, err := os.Lstat(prev); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, unavailable(err, "remove %s", target))
			}
			continue
		}
		if err := restoreFile(target, prev); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// restoreCopy publishes the previous copy prev at target through a staged
// .<name>.restore file and one replaceFile.
func restoreCopy(target, prev string) error {
	tmp := filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".restore")
	if err := copyFileAtomic(prev, tmp); err != nil {
		return err
	}
	if err := replaceFile(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return unavailable(err, "restore %s", target)
	}
	return nil
}

// copyFileAtomic publishes src's bytes at dst with mode 0755.
func copyFileAtomic(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return unavailable(err, "open %s", src)
	}
	defer func() { _ = f.Close() }()
	return runtime.WriteReaderAtomic(dst, f, 0o755)
}

func removeAll(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}
