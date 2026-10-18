//go:build windows

// Purpose: the windows leg of EnsureHome. Windows has no 0700 mode, so the
//
//	home cannot be created and verified private here; this tier-2
//	platform declares a refusal instead of pretending: an EXISTING
//	real directory is returned as-is (its ACL is the operator's),
//	anything else (missing, file, symlink, junction) is
//	ErrProfileHomePermission.
//
// Inputs: the home path from HomePath. Outputs: nil or
//
//	ErrProfileHomePermission.
//
// Constraints: never creates a directory and never reports a mode it did
//
//	not verify.
//
// SPORT: fleet/topology/profile_home_windows/ADD (P1-TOP-01).

package topology

import (
	"io/fs"
	"os"
)

func init() { ensureHomeDir = ensureHomeExisting }

// ensureHomeExisting accepts path only when it is an existing real
// directory: Lstat does not follow a symlink or junction, and a link or
// reparse point (ModeSymlink, ModeIrregular) is refused.
func ensureHomeExisting(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return homePermErr(path, "profile home must already exist on windows: "+err.Error())
	}
	if !info.IsDir() || info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return homePermErr(path, "not a real directory (file, symlink or reparse point)")
	}
	return nil
}
