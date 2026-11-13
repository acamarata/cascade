package background

import (
	"errors"
	"testing"
)

const (
	egressFixtureRepo = "exact-fake/dev-repo"
	egressGoodDiff    = "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"
)

// egressPayload builds a payload with one pass.
func egressPayload(repo string, files []string, diff string) Payload {
	return Payload{Repo: repo, Files: files, Diff: diff, Passes: 1}
}

// runEgressCase runs CheckEgress on the payload, asserts the input is not
// mutated and asserts the error expectation.
func runEgressCase(t *testing.T, payload Payload, wantErr bool) {
	t.Helper()
	// Copy payload to detect mutation
	original := payload
	err := CheckEgress(payload)

	if payload.Repo != original.Repo ||
		len(payload.Files) != len(original.Files) ||
		payload.Diff != original.Diff ||
		payload.Passes != original.Passes {
		t.Errorf("CheckEgress mutated input payload")
	}

	if !wantErr {
		if err != nil {
			t.Errorf("CheckEgress() = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Errorf("CheckEgress() = nil, want ErrEgress")
	} else if !errors.Is(err, ErrEgress) {
		t.Errorf("CheckEgress() = %v, want ErrEgress", err)
	}
}

// TestEgressAllowsExactFakeDevRepo validates that the synthetic fixture
// repo "exact-fake/dev-repo" is eligible and accepts valid payloads,
// while nonmatching, blank or case-variant repos are rejected.
func TestEgressAllowsExactFakeDevRepo(t *testing.T) {
	cases := []struct {
		name    string
		repo    string
		wantErr bool
	}{
		{"exact fake dev repo with valid file and diff", egressFixtureRepo, false},
		{"blank repo", "", true},
		{"case variant repo uppercase", "EXACT-FAKE/DEV-REPO", true},
		{"case variant repo mixed", "Exact-Fake/Dev-Repo", true},
		{"nonmatching repo", "other/repo", true},
		{"nonmatching repo similar name", "exact-fake/dev-repo-extra", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runEgressCase(t, egressPayload(tc.repo, []string{"x.go"}, egressGoodDiff), tc.wantErr)
		})
	}
}

// TestEgressBlocksProtectedFile validates that protected files, unsafe paths,
// and invalid inputs are rejected. The fixture exemption for exact-fake/dev-repo
// does not bypass file, diff or cap checks.
func TestEgressBlocksProtectedFile(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		diff  string
	}{
		{".env file", []string{".env"}, egressGoodDiff},
		{".env.local file", []string{".env.local"}, egressGoodDiff},
		{".git/config file", []string{".git/config"}, egressGoodDiff},
		{".claude/state file", []string{".claude/state"}, egressGoodDiff},
		{".aws/credentials file", []string{".aws/credentials"}, egressGoodDiff},
		{"secrets directory", []string{"secrets/x"}, egressGoodDiff},
		{"TLS.KEY case insensitive suffix", []string{"TLS.KEY"}, egressGoodDiff},
		{"traversal path with dotdot", []string{"../x.go"}, egressGoodDiff},
		{"absolute path", []string{"/etc/passwd"}, egressGoodDiff},
		{"backslash path", []string{"src\\main.go"}, egressGoodDiff},
		{"blank path in files", []string{""}, egressGoodDiff},
		{"duplicate files", []string{"x.go", "x.go"}, egressGoodDiff},
		{"empty files slice", []string{}, egressGoodDiff},
		{
			"diff header path outside files",
			[]string{"x.go"},
			"diff --git a/y.go b/y.go\n--- a/y.go\n+++ b/y.go\n@@ -1 +1 @@\n-old\n+new\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runEgressCase(t, egressPayload(egressFixtureRepo, tc.files, tc.diff), true)
		})
	}
}
