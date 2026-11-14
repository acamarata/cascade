package background

import "testing"

func runEgressTest(t *testing.T, name, diff string, files []string, wantErr bool) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		p := Payload{
			Repo:  "exact-fake/dev-repo",
			Files: files,
			Diff:  diff,
		}
		err := CheckEgress(p)
		if (err != nil) != wantErr {
			t.Errorf("CheckEgress() error = %v, wantErr %v", err, wantErr)
		}
		if err != nil && err != ErrEgress {
			t.Errorf("CheckEgress() returned wrong error: %v", err)
		}
	})
}

// TestEgressRejectsNonLinearDiff_ValidAndEmpty tests valid and empty diffs.
func TestEgressRejectsNonLinearDiff_ValidAndEmpty(t *testing.T) {
	validDiff := "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,4 @@\n line1\n-line2\n+line2modified\n+line3added\n line4\n"
	runEgressTest(t, "valid unified diff", validDiff, []string{"file.go"}, false)
	runEgressTest(t, "empty diff with files", "", []string{"file.go"}, true)
}

// TestEgressRejectsNonLinearDiff_CombinedAndBinary tests combined/binary rejection.
func TestEgressRejectsNonLinearDiff_CombinedAndBinary(t *testing.T) {
	runEgressTest(t, "combined diff header", "diff --cc file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "combined format header", "diff --combined file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "triple at sign line", "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@@ -1,1 -1,1 +1,1 @@@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "binary patch marker", "diff --git a/file.go b/file.go\nGIT binary patch\nliteral 123\n", []string{"file.go"}, true)
	runEgressTest(t, "binary files marker", "diff --git a/file.go b/file.go\nBinary files a/file.go and b/file.go differ\n", []string{"file.go"}, true)
}

// TestEgressRejectsNonLinearDiff_DuplicateAndEncoding tests duplicate/encoding issues.
func TestEgressRejectsNonLinearDiff_DuplicateAndEncoding(t *testing.T) {
	runEgressTest(t, "duplicate header", "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-c\n+d\n", []string{"file.go"}, true)
	runEgressTest(t, "invalid UTF-8", "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\xff\n", []string{"file.go"}, true)
	runEgressTest(t, "NUL in diff", "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\x00\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "CR in diff", "diff --git a/file.go b/file.go\n--- a/file.go\r\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
}

// TestEgressRejectsNonLinearDiff_MissingLines tests missing structural lines.
func TestEgressRejectsNonLinearDiff_MissingLines(t *testing.T) {
	runEgressTest(t, "missing --- line", "diff --git a/file.go b/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "missing +++ line", "diff --git a/file.go b/file.go\n--- a/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "missing hunk", "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n", []string{"file.go"}, true)
	runEgressTest(t, "out of order metadata - +++ before ---", "diff --git a/file.go b/file.go\n+++ b/file.go\n--- a/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
}

// TestEgressRejectsNonLinearDiff_PathMismatch tests path mismatch scenarios.
func TestEgressRejectsNonLinearDiff_PathMismatch(t *testing.T) {
	runEgressTest(t, "mismatched path in header", "diff --git a/file.go b/other.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "mismatched path in ---", "diff --git a/file.go b/file.go\n--- a/other.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "mismatched path in +++", "diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/other.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
}

// TestEgressRejectsNonLinearDiff_MiscInvalid tests miscellaneous invalid cases.
func TestEgressRejectsNonLinearDiff_MiscInvalid(t *testing.T) {
	runEgressTest(t, "unsupported metadata line", "diff --git a/file.go b/file.go\nold mode 100644\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "content before header", "some garbage\ndiff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "quoted path in header", "diff --git \"a/file.go\" \"b/file.go\"\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "rename header", "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n", []string{"new.go"}, true)
	runEgressTest(t, "dev/null in ---", "diff --git a/file.go b/file.go\n--- /dev/null\n+++ b/file.go\n@@ -0,0 +1 @@\n+new\n", []string{"file.go"}, true)
}

// TestEgressRejectsNonLinearDiff_AllowedSet tests allowed-set coverage.
func TestEgressRejectsNonLinearDiff_AllowedSet(t *testing.T) {
	runEgressTest(t, "file not in allowed set", "diff --git a/other.go b/other.go\n--- a/other.go\n+++ b/other.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file.go"}, true)
	runEgressTest(t, "multiple files not all covered", "diff --git a/file1.go b/file1.go\n--- a/file1.go\n+++ b/file1.go\n@@ -1 +1 @@\n-a\n+b\n", []string{"file1.go", "file2.go"}, true)
	runEgressTest(t, "multiple files valid", "diff --git a/file1.go b/file1.go\n--- a/file1.go\n+++ b/file1.go\n@@ -1 +1 @@\n-a\n+b\ndiff --git a/file2.go b/file2.go\n--- a/file2.go\n+++ b/file2.go\n@@ -1 +1 @@\n-c\n+d\n", []string{"file1.go", "file2.go"}, false)
}
