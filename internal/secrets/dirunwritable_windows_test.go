//go:build windows

package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// makeDirGenuinelyUnwritable replaces dir's DACL with a single explicit
// DENY ACE for the current process's user SID, covering FILE_WRITE_DATA
// (add a file) and FILE_APPEND_DATA (add a subdirectory; the same bit
// Windows calls FILE_ADD_SUBDIRECTORY on a directory object). chmod on
// Windows only toggles FILE_ATTRIBUTE_READONLY, which does not block
// creating files inside a directory, so this is the real mechanism.
//
// A DACL deny check can be bypassed entirely by a process holding
// SeBackupPrivilege/SeRestorePrivilege or one running elevated, which some
// CI runners do. Rather than trust the ACE silently, this attempts a real
// write against dir and fails the test loudly if it succeeds: a quietly
// passing test that never actually denied anything would be worthless.
//
// t.Cleanup grants the current user GENERIC_ALL back with an unprotected
// DACL (letting the parent's inherited ACEs re-merge) before returning,
// so t.TempDir's own removal still succeeds; without this the directory
// would be left undeletable and turn a passing test into a failing suite.
func makeDirGenuinelyUnwritable(t *testing.T, dir string) {
	t.Helper()
	sid := currentUserSID(t)

	var pinner runtime.Pinner
	pinner.Pin(sid)
	deny := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA,
		AccessMode:        windows.DENY_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{deny}, nil)
	pinner.Unpin()
	if err != nil {
		t.Fatalf("building the deny ACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatalf("applying the deny ACL: %v", err)
	}
	t.Cleanup(func() { restoreDirWritable(t, dir) })

	probe := filepath.Join(dir, ".dacl-denial-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err == nil {
		_ = os.Remove(probe)
		t.Fatal("the DENY ACE did not take effect: the write succeeded anyway, " +
			"which means this process bypasses DACL checks (SeBackupPrivilege, " +
			"SeRestorePrivilege, or running elevated) and this test cannot prove " +
			"anything under those conditions")
	}
}

// currentUserSID resolves the SID of the process's own token, the trustee
// the deny ACE is written against.
func currentUserSID(t *testing.T) *windows.SID {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("resolving the current user SID: %v", err)
	}
	return user.User.Sid
}

// restoreDirWritable grants the current user GENERIC_ALL back on dir with
// an unprotected DACL, best-effort: this only exists to let t.TempDir
// clean up after itself, so a failure here is reported, not fatal.
func restoreDirWritable(t *testing.T, dir string) {
	t.Helper()
	sid := currentUserSID(t)
	var pinner runtime.Pinner
	pinner.Pin(sid)
	grant := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{grant}, nil)
	pinner.Unpin()
	if err != nil {
		t.Errorf("restoring write access to %s: %v", dir, err)
		return
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Errorf("restoring write access to %s: %v", dir, err)
	}
}
