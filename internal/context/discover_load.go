package context

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: read one tier's instruction file safely: regular files only
//   (never a symlink, directory, FIFO or device), no symlinked .cascade
//   directory, no symlink following, a same-file check against the Lstat,
//   and a hard size bound.
// Inputs: a role, a directory and an ordinal.
// Outputs: a TierRecord; a typed cascade.Error only for genuine I/O failure.
// Constraints: no read of a file above maxTierBytes.
// SPORT: context-engine/discovery.

// maxTierBytes caps a tier file: anything larger is Absent with
// FindingTierTooLarge and is never read.
const maxTierBytes = 1 << 20

// Test hooks: afterTierLstat runs between the Lstat and the open (a swap
// injection point); wrapTierReader wraps the reader over the opened file so a
// test can count reads. Production leaves both as no-ops.
var (
	afterTierLstat = func(string) {}
	wrapTierReader = func(r io.Reader) io.Reader { return r }
)

// loadTier builds the TierRecord for role at dir, reading its instruction
// file when one is present. A dir of "" produces an Absent record with no
// filesystem access at all. <dir>/.cascade must be a real directory and the
// file a regular file (never a symlink, directory, FIFO or device); it is
// opened without following links or blocking, must be the same file
// the Lstat saw, and is read through a maxTierBytes bound.
func loadTier(role TierRole, dir string, ordinal int) (TierRecord, error) {
	rec := TierRecord{Role: role, Ordinal: ordinal}
	if dir == "" {
		rec.Absent = true
		return rec, nil
	}
	rec.Dir = dir
	rec.Path = filepath.Join(dir, tierDirName, tierFileName)

	switch ok, err := realTierDir(dir); {
	case err != nil:
		return TierRecord{}, err
	case !ok:
		rec.Absent = true
		return rec, nil
	}
	info, err := os.Lstat(rec.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		rec.Absent = true
		return rec, nil
	case err != nil:
		return TierRecord{}, wrapTierFSErr(err, "stat tier file "+rec.Path)
	}
	if !info.Mode().IsRegular() {
		rec.Absent = true
		return rec, nil
	}
	// On Windows the FileInfo from os.Lstat loads its file id lazily, on the
	// first os.SameFile call, by re-opening the path. Force that load now so
	// the id is pinned to the file Lstat saw; otherwise a swap after this point
	// would be read into the id and match the file opened later, hiding it.
	// Harmless on unix, where the id is already in the stat result.
	_ = os.SameFile(info, info)
	afterTierLstat(rec.Path)
	return readTierFile(rec, info)
}

// realTierDir reports whether <dir>/.cascade is a real directory. A symlinked
// .cascade (which could point outside dir) or a non-directory is refused, so
// the tier file read always stays inside dir. The error is non-nil only for a
// genuine I/O failure.
func realTierDir(dir string) (bool, error) {
	p := filepath.Join(dir, tierDirName)
	info, err := os.Lstat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, wrapTierFSErr(err, "stat tier dir "+p)
	}
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0, nil
}

// readTierFile opens rec.Path and fills rec.Content, enforcing the size bound
// and the same-file check. lst is the Lstat taken before the open.
func readTierFile(rec TierRecord, lst os.FileInfo) (TierRecord, error) {
	f, err := openNoFollow(rec.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isSymlinkLoop(err) {
			rec.Absent = true
			return rec, nil
		}
		return TierRecord{}, wrapTierFSErr(err, "open tier file "+rec.Path)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return TierRecord{}, wrapTierFSErr(err, "stat tier file "+rec.Path)
	}
	if !os.SameFile(lst, fi) || !fi.Mode().IsRegular() {
		rec.Absent = true
		return rec, nil
	}
	if fi.Size() > maxTierBytes {
		rec.Absent = true
		rec.Findings = append(rec.Findings, FindingTierTooLarge)
		return rec, nil
	}
	content, err := io.ReadAll(io.LimitReader(wrapTierReader(f), maxTierBytes+1))
	if err != nil {
		return TierRecord{}, wrapTierFSErr(err, "read tier file "+rec.Path)
	}
	if len(content) > maxTierBytes {
		rec.Absent = true
		rec.Findings = append(rec.Findings, FindingTierTooLarge)
		return rec, nil
	}
	rec.Content = string(content)
	return rec, nil
}

// wrapTierFSErr classifies a filesystem error into the taxonomy: permission
// errors become KindPermissionDenied, everything else KindUnavailable —
// mirroring internal/daemon/service.wrapFSError's convention for the same
// distinction.
func wrapTierFSErr(err error, msg string) error {
	if os.IsPermission(err) {
		return cascade.Wrap(cascade.KindPermissionDenied, err, "context: "+msg)
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "context: "+msg)
}
