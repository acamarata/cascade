package background

import "testing"

// TestEgressRejectsNonLinearDiff validates that the diff scanner rejects
// malformed, non-linear, or unsafe diff content.
func TestEgressRejectsNonLinearDiff(t *testing.T) {
	validDiff := "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,4 @@\n line1\n-line2\n+line2modified\n+line3added\n line4\n"

	tests := []struct {
		name    string
		diff    string
		files   []string
		wantErr bool
	}{
		{
			name:    "valid unified diff",
			diff:    validDiff,
			files:   []string{"file.go"},
			wantErr: false,
		},
		{
			name:    "empty diff with files",
			diff:    "",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "combined diff header",
			diff:    "diff --cc file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "combined format header",
			diff:    "diff --combined file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "triple at sign line",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@@ -1,1 -1,1 +1,1 @@@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "binary patch marker",
			diff:    "diff --git a/file.go b/file.go\nGIT binary patch\nliteral 123\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "binary files marker",
			diff:    "diff --git a/file.go b/file.go\nBinary files a/file.go and b/file.go differ\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "duplicate header",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-c\n+d\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "invalid UTF-8",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\xff\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "NUL in diff",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\x00\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "CR in diff",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\r\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "missing --- line",
			diff:    "diff --git a/file.go b/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "missing +++ line",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "missing hunk",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "out of order metadata - +++ before ---",
			diff:    "diff --git a/file.go b/file.go\n+++ b/file.go\n--- a/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "mismatched path in header",
			diff:    "diff --git a/file.go b/other.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "mismatched path in ---",
			diff:    "diff --git a/file.go b/file.go\n--- a/other.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "mismatched path in +++",
			diff:    "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/other.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "unsupported metadata line",
			diff:    "diff --git a/file.go b/file.go\nold mode 100644\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "content before header",
			diff:    "some garbage\ndiff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "quoted path in header",
			diff:    "diff --git \"a/file.go\" \"b/file.go\"\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "rename header",
			diff:    "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n",
			files:   []string{"new.go"},
			wantErr: true,
		},
		{
			name:    "dev/null in ---",
			diff:    "diff --git a/file.go b/file.go\n--- /dev/null\n+++ b/file.go\n@@ -0,0 +1 @@\n+new\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "file not in allowed set",
			diff:    "diff --git a/other.go b/other.go\n--- a/other.go\n+++ b/other.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file.go"},
			wantErr: true,
		},
		{
			name:    "multiple files not all covered",
			diff:    "diff --git a/file1.go b/file1.go\n--- a/file1.go\n+++ b/file1.go\n@@ -1 +1 @@\n-a\n+b\n",
			files:   []string{"file1.go", "file2.go"},
			wantErr: true,
		},
		{
			name:    "multiple files valid",
			diff:    "diff --git a/file1.go b/file1.go\n--- a/file1.go\n+++ b/file1.go\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/file2.go b/file2.go\n--- a/file2.go\n+++ b/file2.go\n@@ -1 +1 @@\n-c\n+d\n",
			files:   []string{"file1.go", "file2.go"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Payload{
				Repo:  "exact-fake/dev-repo",
				Files: tt.files,
				Diff:  tt.diff,
			}
			err := CheckEgress(p)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckEgress() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && err != ErrEgress {
				t.Errorf("CheckEgress() returned wrong error: %v", err)
			}
		})
	}
}
