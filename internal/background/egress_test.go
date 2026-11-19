package background

import (
	"errors"
	"testing"
)

// TestEgressAllowsExactFakeDevRepo validates that the synthetic fixture
// repo "exact-fake/dev-repo" is eligible and accepts valid payloads,
// while nonmatching, blank or case-variant repos are rejected.
func TestEgressAllowsExactFakeDevRepo(t *testing.T) {
	goodDiff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"

	cases := []struct {
		name    string
		payload Payload
		wantErr bool
	}{
		{
			name: "exact fake dev repo with valid file and diff",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
			wantErr: false,
		},
		{
			name: "blank repo",
			payload: Payload{
				Repo:   "",
				Files:  []string{"x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
			wantErr: true,
		},
		{
			name: "case variant repo uppercase",
			payload: Payload{
				Repo:   "EXACT-FAKE/DEV-REPO",
				Files:  []string{"x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
			wantErr: true,
		},
		{
			name: "case variant repo mixed",
			payload: Payload{
				Repo:   "Exact-Fake/Dev-Repo",
				Files:  []string{"x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
			wantErr: true,
		},
		{
			name: "nonmatching repo",
			payload: Payload{
				Repo:   "other/repo",
				Files:  []string{"x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
			wantErr: true,
		},
		{
			name: "nonmatching repo similar name",
			payload: Payload{
				Repo:   "exact-fake/dev-repo-extra",
				Files:  []string{"x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Copy payload to detect mutation
			original := tc.payload
			err := CheckEgress(tc.payload)

			// Check mutation
			if tc.payload.Repo != original.Repo ||
				len(tc.payload.Files) != len(original.Files) ||
				tc.payload.Diff != original.Diff ||
				tc.payload.Passes != original.Passes {
				t.Errorf("CheckEgress mutated input payload")
			}

			if tc.wantErr {
				if err == nil {
					t.Errorf("CheckEgress() = nil, want error")
				} else if !errors.Is(err, ErrEgress) {
					t.Errorf("CheckEgress() = %v, want ErrEgress", err)
				}
			} else {
				if err != nil {
					t.Errorf("CheckEgress() = %v, want nil", err)
				}
			}
		})
	}
}

// TestEgressBlocksProtectedFile validates that protected files, unsafe paths,
// and invalid inputs are rejected. The fixture exemption for exact-fake/dev-repo
// does not bypass file, diff or cap checks.
func TestEgressBlocksProtectedFile(t *testing.T) {
	goodDiff := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n"

	cases := []struct {
		name    string
		payload Payload
	}{
		{
			name: ".env file",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{".env"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: ".env.local file",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{".env.local"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: ".git/config file",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{".git/config"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: ".claude/state file",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{".claude/state"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: ".aws/credentials file",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{".aws/credentials"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "secrets directory",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"secrets/x"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "TLS.KEY case insensitive suffix",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"TLS.KEY"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "traversal path with dotdot",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"../x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "absolute path",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"/etc/passwd"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "backslash path",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"src\\main.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "blank path in files",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{""},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "duplicate files",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"x.go", "x.go"},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "empty files slice",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{},
				Diff:   goodDiff,
				Passes: 1,
			},
		},
		{
			name: "diff header path outside files",
			payload: Payload{
				Repo:   "exact-fake/dev-repo",
				Files:  []string{"x.go"},
				Diff:   "diff --git a/y.go b/y.go\n--- a/y.go\n+++ b/y.go\n@@ -1 +1 @@\n-old\n+new\n",
				Passes: 1,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Copy payload to detect mutation
			original := tc.payload
			err := CheckEgress(tc.payload)

			// Check mutation
			if tc.payload.Repo != original.Repo ||
				len(tc.payload.Files) != len(original.Files) ||
				tc.payload.Diff != original.Diff ||
				tc.payload.Passes != original.Passes {
				t.Errorf("CheckEgress mutated input payload")
			}

			if err == nil {
				t.Errorf("CheckEgress() = nil, want ErrEgress")
			} else if !errors.Is(err, ErrEgress) {
				t.Errorf("CheckEgress() = %v, want ErrEgress", err)
			}
		})
	}
}
