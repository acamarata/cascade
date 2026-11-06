package service

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultLingerDir holds systemd's per-user linger markers.
const DefaultLingerDir = "/var/lib/systemd/linger"

// LingerEnabled reports whether the user's linger marker exists.
// Unreadable state refuses installation rather than assuming linger is on.
func LingerEnabled(lingerDir, user string) (bool, error) {
	if user == "" || strings.Contains(user, "/") {
		return false, cascade.New(cascade.KindInvalidInput, "service: invalid user name for linger check")
	}
	_, err := os.Stat(filepath.Join(lingerDir, user))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) && lingerDirAbsentOrDir(lingerDir) {
		return false, nil
	}
	return false, cascade.Newf(cascade.KindUnavailable, "service: cannot read linger state for user %s", user)
}

// lingerDirAbsentOrDir reports whether a missing user marker means "no
// linger". A linger path that exists but is not a directory is unreadable
// state; some platforms report a lookup under a regular file as not-exist.
func lingerDirAbsentOrDir(lingerDir string) bool {
	info, err := os.Stat(lingerDir)
	if err != nil {
		return os.IsNotExist(err)
	}
	return info.IsDir()
}

// ErrNoLinger explains why an operator must enable linger before install.
func ErrNoLinger(user string) error {
	return cascade.Newf(cascade.KindUnavailable,
		"node service: user %q has no systemd linger, so the node agent stops at logout and does not start at boot; run: sudo loginctl enable-linger %s", user, user)
}
