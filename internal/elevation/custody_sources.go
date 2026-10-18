// Purpose: own the production custody source registry.
// Inputs: platform and directory. Outputs: source descriptors.
// Constraints: Linux PAM is not protected custody. The linux passphrase
// presence source is registered by keystore_presence_tty_linux.go and is
// absent from every other platform. SPORT: elevation sources.
//
// The second half of this file is the trust rule for WHERE the darwin helper
// may live (checkHelperLocation), kept here because it decides whether the
// darwin-helper source may be used at all.

package elevation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/acamarata/cascade/pkg/cascade"
)

// PresenceAuditor records a presence enrolment before the key file is
// replaced. Begin writes the intent record and returns a finish callback
// that records the outcome (nil = the enrolment completed). A Begin error
// refuses the enrolment.
type PresenceAuditor interface {
	Begin(fingerprint, kind string) (finish func(error), err error)
}

// presenceSource and presenceEnroll are set by the linux-only presence
// files; they stay nil on darwin and windows, so no presence code is built
// there.
var (
	presenceSource *CustodySource
	presenceEnroll func(dir string, trust Backend, clock Clock, aud PresenceAuditor) (string, error)
)

// EnrollPresence creates or rotates the linux presence key: it prompts on
// the controlling terminal, audits before the file is replaced, and writes
// the trust record. It returns the new key's fingerprint. Any other
// platform refuses.
func EnrollPresence(dataDir string, trust Backend, clock Clock, aud PresenceAuditor) (string, error) {
	if presenceEnroll == nil {
		return "", cascade.New(cascade.KindUnsupported, "elevation: the passphrase presence tier is available on linux only")
	}
	return presenceEnroll(dataDir, trust, clock, aud)
}

// DefaultSources registers the supported production custody sources.
func DefaultSources() []CustodySource { return sourcesForOS(runtime.GOOS, NewKeystore) }

// sourcesForOS lists the custody sources for one OS, highest rank first.
func sourcesForOS(goos string, platform func() ElevationKeystore) []CustodySource {
	if goos == "windows" {
		return nil
	}
	sources := []CustodySource{}
	if goos == "darwin" {
		sources = append(sources, CustodySource{Tier: CustodyPlatform, Name: "platform", Open: func(string) (ElevationKeystore, bool) {
			ks := platform()
			return ks, ks != nil && ks.IsAvailable()
		}})
	}
	if goos == "linux" {
		sources = append(sources, CustodySource{Tier: CustodyFile, Name: "linux-pam", Open: func(string) (ElevationKeystore, bool) { ks := platform(); return ks, ks != nil && ks.IsAvailable() }})
		if presenceSource != nil {
			sources = append(sources, *presenceSource)
		}
	}
	sources = append(sources, CustodySource{Tier: CustodyFile, Name: "file", Open: func(dir string) (ElevationKeystore, bool) {
		return fileCustodyKeystore(dir), dir != "" && (fileKeyExists(dir) || devkeysBuild)
	}})
	return sources
}

// checkHelperLocation refuses a helper another user could replace: the file
// and every directory from its own up to the root must be owned by this user
// or root, not group- or world-writable (unless sticky), and carry no
// extended ACL entry that allows anyone to write.
func checkHelperLocation(helper string) error {
	// codesign echoes the path into the output we parse; a control character
	// (a newline) could forge lines in it.
	if strings.ContainsFunc(helper, unicode.IsControl) {
		return errors.New("path contains a control character")
	}
	if err := aclAllowsWrite(helper); err != nil {
		return err
	}
	return checkDirChain(filepath.Dir(helper))
}

// checkDirChain checks dir and every ancestor up to the root.
func checkDirChain(dir string) error {
	for {
		if err := checkPrivateBase(dir); err != nil {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

// checkPrivateBase refuses a directory other users could rename entries in:
// it must be a directory owned by this user or root and not group- or
// world-writable (unless sticky).
func checkPrivateBase(base string) error {
	fi, err := os.Stat(base)
	switch {
	case err != nil:
		return err
	case !fi.IsDir():
		return errors.New("temp directory is not a directory")
	case platformFileOwnerTrusted == nil || !platformFileOwnerTrusted(fi):
		return errors.New("temp directory is owned by another user")
	case fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0:
		return errors.New("temp directory is writable by other users")
	case platformVolumeTrusted != nil && !platformVolumeTrusted(base):
		return errors.New("directory is on a volume that ignores file ownership")
	}
	return aclAllowsWrite(base)
}

// lsACL lists path with its extended ACL entries (macOS: /bin/ls -lde).
var lsACL = func(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), codesignTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ls", "-lde", path)
	cmd.Env = []string{}
	cmd.Dir = "/"
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	return codesignResult(&out, cmd.Run())
}

// aclEntry matches an `ls -e` entry line such as ` 0: user:x allow write`.
var aclEntry = regexp.MustCompile(`^\s*\d+: (\S+) allow (\S+)\s*$`)

// aclWriteRights are the ACL rights that let a user change or replace entries.
var aclWriteRights = map[string]bool{
	"write": true, "add_file": true, "append": true, "add_subdirectory": true, "delete": true,
	"delete_child": true, "writeattr": true, "writeextattr": true, "writesecurity": true, "chown": true,
}

// aclAllowsWrite is nil when path carries no extended ACL entry that allows
// anyone write-like rights. Mode bits do not show macOS ACLs, so on darwin
// the listing is read (and an unreadable or unparsable one is refused).
func aclAllowsWrite(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	out, err := lsACL(path)
	if err != nil {
		return err
	}
	return unsafeACL(string(out))
}

// unsafeACL is non-nil when an `ls -e` listing has an allow entry with a write right.
func unsafeACL(listing string) error {
	for _, line := range strings.Split(listing, "\n") {
		if !strings.Contains(line+" ", " allow ") {
			continue
		}
		m := aclEntry.FindStringSubmatch(line)
		if m == nil {
			return errors.New("unparsable ACL entry")
		}
		for _, right := range strings.Split(m[2], ",") {
			if aclWriteRights[right] {
				return errors.New("an ACL entry allows writing")
			}
		}
	}
	return nil
}
