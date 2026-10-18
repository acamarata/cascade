//go:build unix

// Purpose: the unix leg of EnsureHome. profiles/ and the home are opened
//
//	with O_NOFOLLOW|O_DIRECTORY and every check and chmod goes through
//	the open descriptor, so a path swapped for a symlink after the check
//	can never redirect a chmod onto another directory.
//
// Inputs: the home path from HomePath. Outputs: nil or
//
//	ErrProfileHomePermission.
//
// Constraints: profiles/ and the home must be real directories owned by the
//
//	effective user; both end at mode 0700, verified after the chmod.
//
// SPORT: fleet/topology/profile_home_unix/ADD (P1-TOP-01).

package topology

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func init() { ensureHomeDir = ensureHomeUnix }

// profileHomeMode is the only mode a profile home may carry.
const profileHomeMode fs.FileMode = 0o700

// homeModeMask selects every bit that makes up a directory's mode: the
// permission bits plus setuid, setgid and sticky, so a home at 0700 with a
// special bit is not mistaken for 0700.
const homeModeMask = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// effectiveUID is the owner every home must have; a seam for the owner test.
var effectiveUID = os.Geteuid

// ensureHomeUnix creates profiles/ and the home (0700) and secures both.
func ensureHomeUnix(path string) error {
	profiles := filepath.Dir(path)
	if err := os.MkdirAll(profiles, profileHomeMode); err != nil {
		return homePermErr(profiles, err.Error())
	}
	if err := secureDir(profiles); err != nil {
		return err
	}
	if err := os.Mkdir(path, profileHomeMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return homePermErr(path, err.Error())
	}
	return secureDir(path)
}

// secureDir opens path without following a link, requires a directory owned
// by the effective user, and makes it mode 0700 through the descriptor
// (leaving a correct mode untouched), then verifies the result.
func secureDir(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return homePermErr(path, "not a real directory: "+err.Error())
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return homePermErr(path, err.Error())
	}
	if err := requireOwner(path, info); err != nil {
		return err
	}
	if info.Mode()&homeModeMask != profileHomeMode {
		if err := chmodHome(f, profileHomeMode); err != nil {
			return homePermErr(path, err.Error())
		}
		if info, err = f.Stat(); err != nil {
			return homePermErr(path, err.Error())
		}
	}
	if !info.IsDir() || info.Mode()&homeModeMask != profileHomeMode {
		return homePermErr(path, "mode is not 0700 after chmod")
	}
	return nil
}

// requireOwner refuses a directory not owned by the effective user.
func requireOwner(path string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != effectiveUID() {
		return homePermErr(path, "directory is not owned by the effective user")
	}
	return nil
}
