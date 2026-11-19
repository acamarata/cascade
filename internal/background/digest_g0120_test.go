package background

import "testing"

func TestDigestValidApproval(t *testing.T) {
	a := Approval{
		Command:   "ls -la",
		Folder:    "/var/log",
		GrantHash: "abcdef1234567890",
	}
	digest := ApprovalDigest(a)
	if !VerifyApprovalDigest(a, digest) {
		t.Fatal("valid approval failed digest verification")
	}
}

func TestDigestTamperedCommand(t *testing.T) {
	a := Approval{
		Command:   "ls -la",
		Folder:    "/var/log",
		GrantHash: "abcdef1234567890",
	}
	digest := ApprovalDigest(a)
	a.Command = "rm -rf /"
	if VerifyApprovalDigest(a, digest) {
		t.Fatal("tampered command should not verify")
	}
}

func TestDigestTamperedFolder(t *testing.T) {
	a := Approval{
		Command:   "ls -la",
		Folder:    "/var/log",
		GrantHash: "abcdef1234567890",
	}
	digest := ApprovalDigest(a)
	a.Folder = "/etc"
	if VerifyApprovalDigest(a, digest) {
		t.Fatal("tampered folder should not verify")
	}
}

func TestDigestTamperedGrantHash(t *testing.T) {
	a := Approval{
		Command:   "ls -la",
		Folder:    "/var/log",
		GrantHash: "abcdef1234567890",
	}
	digest := ApprovalDigest(a)
	a.GrantHash = "0000000000000000"
	if VerifyApprovalDigest(a, digest) {
		t.Fatal("tampered grant hash should not verify")
	}
}
