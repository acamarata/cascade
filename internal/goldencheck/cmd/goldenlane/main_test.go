package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/goldencheck"
	"github.com/acamarata/cascade/internal/output"
)

// runWith runs the tool with its output captured in the two buffers.
func runWith(stdout, stderr *bytes.Buffer, args []string) int {
	return run(args, output.New(stdout, stderr, false, false, false, true))
}

// realRoot is the module root, found from this file's location.
func realRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller info")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..")
}

func TestPlanPrintsOneLinePerOwnerRow(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWith(&stdout, &stderr, []string{"plan", "--root", realRoot(t)}); code != 0 {
		t.Fatalf("plan exit %d: %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != len(goldencheck.Owners) {
		t.Fatalf("%d plan lines for %d owner rows", len(lines), len(goldencheck.Owners))
	}
	for _, line := range lines {
		if len(strings.Split(line, "\t")) != 3 {
			t.Fatalf("plan line is not package<TAB>regex<TAB>root: %q", line)
		}
	}
}

func TestPlanFailsOnAnUnmappedRoot(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":                           "module x\n",
		"stray/testdata/v1-goldens/a.json": "{}\n",
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runWith(&stdout, &stderr, []string{"plan", "--root", dir}); code != 1 || !strings.Contains(stderr.String(), "stray/testdata/v1-goldens") {
		t.Fatalf("exit %d stderr %q", code, stderr.String())
	}
	if code := runWith(&stdout, &stderr, []string{"plan", "--root", filepath.Join(dir, "nope")}); code != 1 {
		t.Fatalf("a non-module root must exit 1, got %d", code)
	}
}

// passingEvents is a one-test passing stream for the named test.
func passingEvents(name string) string {
	return fmt.Sprintf("{\"Action\":\"run\",\"Test\":%q}\n{\"Action\":\"pass\",\"Test\":%q}\n{\"Action\":\"pass\"}\n", name, name)
}

func TestReportExitCodesFollowTheRows(t *testing.T) {
	root := realRoot(t)
	rows, err := plan(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Every row gets a passing stream for its first named test; the regex is
	// ^(A|B)$ or ^A$, so a stream naming only A fails multi-test rows. Feed
	// every named test instead.
	for i, row := range rows {
		var b strings.Builder
		for _, name := range strings.Split(strings.Trim(row.RunRegex, "^()$"), "|") {
			b.WriteString(passingEvents(name))
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", i+1)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "report.json")
	var stdout, stderr bytes.Buffer
	if code := runWith(&stdout, &stderr, []string{"report", "--root", root, "--dir", dir, "--out", out}); code != 0 {
		t.Fatalf("all rows pass: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if data, err := os.ReadFile(out); err != nil || !strings.Contains(string(data), `"ok": true`) {
		t.Fatalf("report file: %v %s", err, data)
	}
	if err := os.Remove(filepath.Join(dir, "3.json")); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	code := runWith(&stdout, &stderr, []string{"report", "--root", root, "--dir", dir, "--out", out})
	if code != 1 || !strings.Contains(stdout.String(), "FAIL "+rows[2].Root) {
		t.Fatalf("a row without output must fail naming it: exit %d\n%s", code, stdout.String())
	}
}

func TestRunUsageAndWriteErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{nil, {"bogus"}, {"plan", "--nope"}, {"report", "--root", realRoot(t)}} {
		if code := runWith(&stdout, &stderr, args); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
	}
	bad := filepath.Join(t.TempDir(), "missing", "report.json")
	if code := runWith(&stdout, &stderr, []string{"report", "--root", realRoot(t), "--dir", t.TempDir(), "--out", bad}); code != 1 {
		t.Errorf("an unwritable report must exit 1, got %d", code)
	}
}
