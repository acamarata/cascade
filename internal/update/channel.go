package update

// Purpose: resolve how this binary was installed, which decides whether
//   self-update may act at all.
// Inputs: the buildinfo install-channel stamp, the running executable path
//   and the install receipt path.
// Outputs: one Channel and a human reason naming the deciding input.
// Constraints: the order is fixed: a valid non-empty stamp, then a Homebrew
//   Caskroom path, then the receipt's channel, then manual. An invalid stamp
//   or an unreadable receipt is ignored here; Check reports a broken receipt
//   itself. No network, no writes.
// SPORT: internal/update ResolveChannel/ADDED.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/buildinfo"
)

// Channel is the install channel self-update resolves.
type Channel string

// The install channels (the buildinfo.ValidInstallChannels vocabulary).
const (
	ChannelScript      Channel = "script"
	ChannelBrew        Channel = "brew"
	ChannelOCI         Channel = "oci"
	ChannelNodeManaged Channel = "node-managed"
	ChannelManual      Channel = "manual"
)

// caskroomMarker is the path segment every Homebrew cask install carries.
const caskroomMarker = "/Caskroom/cascade/"

// receiptFileName is the receipt install.sh writes under the cascade root.
const receiptFileName = "install-receipt.json"

// Env carries the channel inputs. Getenv resolves the receipt path when
// ReceiptPath is empty (CASCADE_HOME, else HOME or USERPROFILE).
type Env struct {
	Stamp       string
	Executable  func() (string, error)
	ReceiptPath string
	Getenv      func(string) string
}

// ResolveChannel returns the install channel and the reason it was chosen.
func ResolveChannel(e Env) (Channel, string) {
	if e.Stamp != "" && buildinfo.ValidInstallChannels[e.Stamp] {
		return Channel(e.Stamp), "build stamp install_channel=" + e.Stamp
	}
	if exe := resolvedExecutable(e.Executable); strings.Contains(filepath.ToSlash(exe), caskroomMarker) {
		return ChannelBrew, "executable is under a Homebrew Caskroom: " + exe
	}
	path := e.receiptPath()
	if r, err := ReadReceipt(path); err == nil {
		return Channel(r.Channel), "install receipt " + path
	}
	return ChannelManual, "no usable install receipt at " + path
}

// receiptPath is ReceiptPath, or the default location derived from Getenv.
func (e Env) receiptPath() string {
	if e.ReceiptPath != "" || e.Getenv == nil {
		return e.ReceiptPath
	}
	return DefaultReceiptPath(e.Getenv)
}

// DefaultReceiptPath is <CASCADE_HOME or ~/.cascade>/install-receipt.json,
// the path install.sh writes. An unresolvable home yields "".
func DefaultReceiptPath(getenv func(string) string) string {
	root := getenv("CASCADE_HOME")
	if root == "" {
		home := getenv("HOME")
		if home == "" {
			home = getenv("USERPROFILE")
		}
		if home == "" {
			return ""
		}
		root = filepath.Join(home, ".cascade")
	}
	return filepath.Join(root, receiptFileName)
}

// resolvedExecutable follows symlinks (a brew shim links into the
// Caskroom); a path that does not resolve is used as given.
func resolvedExecutable(exe func() (string, error)) string {
	if exe == nil {
		return ""
	}
	p, err := exe()
	if err != nil || p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if _, err := os.Lstat(p); err != nil {
		return filepath.Clean(p)
	}
	return p
}
