// Purpose: the canary scan of git-ignored paths: a directory such as build/
//   is one `git status --ignored` entry, so it is walked, never followed
//   through a symlink, and never listed as an artifact.
// Inputs: the leased root, a root-relative ignored entry, the canary forms.
// Outputs: nil, ErrCredentialCanary, or a fail-closed unscannable error for
//   a regular file or directory that cannot be read.
// Constraints: sockets, FIFOs and devices are skipped (they hold no content a
//   job wrote), so an ignored cache directory never fails a clean run.
// SPORT: providers.agents.claude/ADD (P1-AGT-01).

package claude

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// scanIgnored scans one git-ignored entry (a file, or a whole directory such
// as build/) for the canary. Regular files are streamed and symlink target
// text is checked; a socket, FIFO or device holds no content a job wrote and
// is skipped, so an ignored cache directory never fails a clean run. A
// symlink is never followed, whether it is the entry or sits inside it.
func scanIgnored(ctx context.Context, root, rel string, forms [][]byte) error {
	path := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return unscannable(rel, err)
	case !fi.IsDir():
		return scanIgnoredLeaf(ctx, root, rel, fi.Mode(), forms)
	}
	return filepath.WalkDir(path, func(p string, de fs.DirEntry, werr error) error {
		if werr != nil {
			return unscannable(rel, werr)
		}
		sub, _ := filepath.Rel(root, p)
		if de.IsDir() {
			if containsCanary([]byte(sub), forms) {
				return ErrCredentialCanary
			}
			return nil
		}
		return scanIgnoredLeaf(ctx, root, filepath.ToSlash(sub), de.Type(), forms)
	})
}

// scanIgnoredLeaf scans one non-directory entry of an ignored path.
func scanIgnoredLeaf(ctx context.Context, root, rel string, mode fs.FileMode, forms [][]byte) error {
	if containsCanary([]byte(rel), forms) {
		return ErrCredentialCanary
	}
	if mode.IsRegular() || mode&fs.ModeSymlink != 0 {
		return scanArtifact(ctx, root, rel, forms)
	}
	return nil
}
