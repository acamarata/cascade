// Purpose: tests for the auth-source checks and the link step of
// ProvisionRuntimeProfile: replaceable ancestors, a loosened or swapped file
// between the check and the link, and a harness id that escapes the base.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-15.

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestProvisionRefusesReplaceableAncestor: a directory above the auth file's
// own directory that another user could write (group or world, no sticky bit)
// lets that user swap the whole tree, so the source is refused.
func TestProvisionRefusesReplaceableAncestor(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	b := fakeBinding("fake")
	outer := filepath.Join(t.TempDir(), "outer")
	vendor := filepath.Join(outer, "vendor")
	if err := os.MkdirAll(vendor, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(vendor, "auth.json")
	if err := os.WriteFile(src, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outer, 0o707); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "profiles")
	_, err := ProvisionRuntimeProfile(base, b, subAuth(src), "job-1")
	wantEntitlement(t, err, "can be written by another user")
	if _, statErr := os.Lstat(filepath.Join(base, "fake", "job-1")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused spawn left its profile behind (%v)", statErr)
	}
	// a sticky world-writable ancestor (like /tmp) is fine: entries in it cannot be renamed by another user.
	if err := os.Chmod(outer, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionRuntimeProfile(base, b, subAuth(src), "job-2"); err != nil {
		t.Fatalf("Provision under a sticky ancestor: %v", err)
	}
	// a group-writable ancestor is accepted (the daemon user's own group).
	if err := os.Chmod(outer, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionRuntimeProfile(base, b, subAuth(src), "job-3"); err != nil {
		t.Fatalf("Provision under a group-writable ancestor: %v", err)
	}
}

// TestProvisionRefusesLooseOwnDirectory: the auth file's own directory may not
// be group writable, and a sticky bit does not excuse a world-writable one
// (the looser ancestor rule must not apply to it).
func TestProvisionRefusesLooseOwnDirectory(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	b := fakeBinding("fake")
	for name, mode := range map[string]os.FileMode{"group writable": 0o770, "sticky world writable": os.ModeSticky | 0o777} {
		src := authFile(t, 0o600)
		if err := os.Chmod(filepath.Dir(src), mode); err != nil {
			t.Fatal(err)
		}
		_, err := ProvisionRuntimeProfile(filepath.Join(t.TempDir(), "profiles"), b, subAuth(src), "job-1")
		if err == nil || !strings.Contains(err.Error(), "writable by the daemon user alone") {
			t.Fatalf("%s own directory: err = %v, want a refusal naming the own directory rule", name, err)
		}
	}
}

// TestProvisionRefusesRelativeAuthSource: the path that was checked must be
// the path that is linked, so a relative one is refused up front.
func TestProvisionRefusesRelativeAuthSource(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	_, err := ProvisionRuntimeProfile(filepath.Join(t.TempDir(), "profiles"), fakeBinding("fake"), subAuth("rel/auth.json"), "job-1")
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative auth source: err = %v, want KindInvalidInput naming an absolute path", err)
	}
}

// TestLinkAuthSourceRefusesLoosenedFile: the file passes the check, then its
// mode is loosened before the link; the same inode must not be accepted.
func TestLinkAuthSourceRefusesLoosenedFile(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	src := authFile(t, 0o600)
	checked, err := checkAuthSource(src)
	if err != nil {
		t.Fatalf("checkAuthSource: %v", err)
	}
	if err := os.Chmod(src, 0o666); err != nil {
		t.Fatal(err)
	}
	configDir := t.TempDir()
	wantEntitlement(t, linkAuthSource(configDir, checked), "changed while it was being linked")
}

// TestProvisionRefusesEscapingHarnessName: a harness id that is a path
// component such as ".." must not move the profile out of the base.
func TestProvisionRefusesEscapingHarnessName(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	for _, id := range []string{"..", ".", "a..b/c"} {
		b := fakeBinding("fake")
		b.Harness = HarnessID(id)
		a := DriverAuth{Harness: HarnessID(id), Class: CredentialAPIKey, KeyEnv: "SRC"}
		base := filepath.Join(t.TempDir(), "state", "profiles")
		if _, err := ProvisionRuntimeProfile(base, b, a, "j"); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("harness %q: err = %v, want KindInvalidInput", id, err)
		}
		if _, err := os.Lstat(filepath.Dir(base)); err == nil {
			t.Fatalf("harness %q: a refused provision created %s", id, filepath.Dir(base))
		}
	}
}

// TestLinkAuthSourceRefusesSwappedFile: the file passes the check and is then
// replaced by another file at the same path; the link is refused.
func TestLinkAuthSourceRefusesSwappedFile(t *testing.T) {
	skipWindowsProfile(t)
	isolateHome(t)
	src := authFile(t, 0o600)
	checked, err := checkAuthSource(src)
	if err != nil {
		t.Fatalf("checkAuthSource: %v", err)
	}
	planted := src + ".new" // created while the old file still exists, so it cannot reuse its inode
	if err := os.WriteFile(planted, []byte(`{"token":"synthetic-planted-body"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(planted, src); err != nil {
		t.Fatal(err)
	}
	wantEntitlement(t, linkAuthSource(t.TempDir(), checked), "changed while it was being linked")
}

// fakeDirInfo is a directory entry owned by an arbitrary uid, so the
// ownership rule can be pinned without a second user on the machine.
type fakeDirInfo struct {
	os.FileInfo
	uid uint32
}

func (f fakeDirInfo) Sys() any { return &struct{ Uid uint32 }{f.uid} } //nolint:revive // Uid mirrors syscall.Stat_t, which ownerUID reads by field name

// TestDirProblemRefusesForeignOwner: a directory owned by neither the daemon
// user nor root is refused, own directory or ancestor, whatever its mode.
func TestDirProblemRefusesForeignOwner(t *testing.T) {
	skipWindowsProfile(t)
	dirInfo, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	foreign := fakeDirInfo{dirInfo, uint32(os.Geteuid()) + 4242}
	for _, own := range []bool{true, false} {
		if why := dirProblem(foreign, nil, own); !strings.Contains(why, "neither the daemon user nor root") {
			t.Fatalf("own=%v: dirProblem(foreign owner) = %q, want the ownership refusal", own, why)
		}
	}
	if why := dirProblem(fakeDirInfo{dirInfo, uint32(os.Geteuid())}, nil, true); why != "" {
		t.Fatalf("dirProblem(daemon-owned private dir) = %q, want it accepted", why)
	}
}
