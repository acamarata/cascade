package docket

import (
	"errors"
	"testing"
)

// fakeRunner implements Runner for testing.
type fakeRunner struct {
	output []byte
	err    error
}

func (f *fakeRunner) Run(_ string, args []string) ([]byte, error) {
	return f.output, f.err
}

// TestStatusClean verifies that a clean repository returns Clean=true with zero counts.
func TestStatusClean(t *testing.T) {
	runner := &fakeRunner{
		output: []byte("## main\n"),
		err:    nil,
	}

	report, err := Status("/fake/dir", runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Branch != "main" {
		t.Errorf("expected Branch 'main', got '%s'", report.Branch)
	}
	if report.Staged != 0 {
		t.Errorf("expected Staged 0, got %d", report.Staged)
	}
	if report.Modified != 0 {
		t.Errorf("expected Modified 0, got %d", report.Modified)
	}
	if report.Untracked != 0 {
		t.Errorf("expected Untracked 0, got %d", report.Untracked)
	}
	if !report.Clean {
		t.Errorf("expected Clean true, got false")
	}
}

// TestStatusDirtyCounts verifies that staged, modified, and untracked files are counted correctly.
func TestStatusDirtyCounts(t *testing.T) {
	runner := &fakeRunner{
		output: []byte("## main\nM a.go\n M b.go\n?? c.go\n"),
		err:    nil,
	}

	report, err := Status("/fake/dir", runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Branch != "main" {
		t.Errorf("expected Branch 'main', got '%s'", report.Branch)
	}
	if report.Staged != 1 {
		t.Errorf("expected Staged 1, got %d", report.Staged)
	}
	if report.Modified != 1 {
		t.Errorf("expected Modified 1, got %d", report.Modified)
	}
	if report.Untracked != 1 {
		t.Errorf("expected Untracked 1, got %d", report.Untracked)
	}
	if report.Clean {
		t.Errorf("expected Clean false, got true")
	}
}

// TestStatusNotRepoFailsClosed verifies that a non-repository returns ErrNotRepo and zero WorkspaceStatusReport.
func TestStatusNotRepoFailsClosed(t *testing.T) {
	runner := &fakeRunner{
		output: []byte("fatal: not a git repository"),
		err:    errors.New("exit status 128"),
	}

	report, err := Status("/fake/dir", runner)
	if !errors.Is(err, ErrNotRepo) {
		t.Errorf("expected ErrNotRepo, got %v", err)
	}
	if report.Branch != "" {
		t.Errorf("expected empty Branch, got '%s'", report.Branch)
	}
	if report.Staged != 0 || report.Modified != 0 || report.Untracked != 0 {
		t.Errorf("expected zero counts, got Staged=%d Modified=%d Untracked=%d",
			report.Staged, report.Modified, report.Untracked)
	}
	if report.Clean {
		t.Errorf("expected Clean false, got true")
	}
}

// TestStatusRunnerError verifies that an I/O error returns a wrapped error and zero WorkspaceStatusReport.
func TestStatusRunnerError(t *testing.T) {
	runner := &fakeRunner{
		output: nil,
		err:    errors.New("I/O error"),
	}

	report, err := Status("/fake/dir", runner)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrNotRepo) {
		t.Error("expected wrapped error, not ErrNotRepo")
	}
	if report.Branch != "" {
		t.Errorf("expected empty Branch, got '%s'", report.Branch)
	}
	if report.Staged != 0 || report.Modified != 0 || report.Untracked != 0 {
		t.Errorf("expected zero counts, got Staged=%d Modified=%d Untracked=%d",
			report.Staged, report.Modified, report.Untracked)
	}
	if report.Clean {
		t.Errorf("expected Clean false, got true")
	}
}
