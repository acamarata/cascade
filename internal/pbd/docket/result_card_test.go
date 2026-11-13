package docket

import (
	"errors"
	"testing"
)

func TestResultCardEmptyCommitRejected(t *testing.T) {
	// Test nil Files
	out, err := Render(CommitInfo{Files: nil, Decls: []string{"decl1"}})
	if out != "" {
		t.Errorf("expected empty output for nil Files, got %q", out)
	}
	if !errors.Is(err, ErrCardEmpty) {
		t.Errorf("expected ErrCardEmpty, got %v", err)
	}
	if err.Error() != "CARD_EMPTY" {
		t.Errorf("expected Error() CARD_EMPTY, got %q", err.Error())
	}

	// Test empty Files
	out, err = Render(CommitInfo{Files: []string{}, Decls: []string{"decl1"}})
	if out != "" {
		t.Errorf("expected empty output for empty Files, got %q", out)
	}
	if !errors.Is(err, ErrCardEmpty) {
		t.Errorf("expected ErrCardEmpty, got %v", err)
	}

	// Test empty entry in Files
	out, err = Render(CommitInfo{Files: []string{""}, Decls: []string{}})
	if out != "" {
		t.Errorf("expected empty output for empty entry, got %q", out)
	}
	if !errors.Is(err, ErrCardInput) {
		t.Errorf("expected ErrCardInput for empty entry, got %v", err)
	}
	if err.Error() != "CARD_INVALID" {
		t.Errorf("expected Error() CARD_INVALID, got %q", err.Error())
	}

	// Test duplicate entry in Files
	out, err = Render(CommitInfo{Files: []string{"a", "a"}, Decls: []string{}})
	if out != "" {
		t.Errorf("expected empty output for duplicate entry, got %q", out)
	}
	if !errors.Is(err, ErrCardInput) {
		t.Errorf("expected ErrCardInput for duplicate entry, got %v", err)
	}

	// Test invalid UTF-8 in Files
	out, err = Render(CommitInfo{Files: []string{"\xff"}, Decls: []string{}})
	if out != "" {
		t.Errorf("expected empty output for invalid UTF-8, got %q", out)
	}
	if !errors.Is(err, ErrCardInput) {
		t.Errorf("expected ErrCardInput for invalid UTF-8, got %v", err)
	}

	// Test CR in Files
	out, err = Render(CommitInfo{Files: []string{"a\rb"}, Decls: []string{}})
	if out != "" {
		t.Errorf("expected empty output for CR, got %q", out)
	}
	if !errors.Is(err, ErrCardInput) {
		t.Errorf("expected ErrCardInput for CR, got %v", err)
	}

	// Test LF in Files
	out, err = Render(CommitInfo{Files: []string{"a\nb"}, Decls: []string{}})
	if out != "" {
		t.Errorf("expected empty output for LF, got %q", out)
	}
	if !errors.Is(err, ErrCardInput) {
		t.Errorf("expected ErrCardInput for LF, got %v", err)
	}

	// Test NUL in Files
	out, err = Render(CommitInfo{Files: []string{"a\x00b"}, Decls: []string{}})
	if out != "" {
		t.Errorf("expected empty output for NUL, got %q", out)
	}
	if !errors.Is(err, ErrCardInput) {
		t.Errorf("expected ErrCardInput for NUL, got %v", err)
	}

	// Test duplicate entry in Decls
	out, err = Render(CommitInfo{Files: []string{"a"}, Decls: []string{"b", "b"}})
	if out != "" {
		t.Errorf("expected empty output for duplicate in Decls, got %q", out)
	}
	if !errors.Is(err, ErrCardInput) {
		t.Errorf("expected ErrCardInput for duplicate in Decls, got %v", err)
	}

	// Test identical text across lists is permitted
	out, err = Render(CommitInfo{Files: []string{"same"}, Decls: []string{"same"}})
	if err != nil {
		t.Errorf("expected no error for identical text across lists, got %v", err)
	}
	if out == "" {
		t.Errorf("expected non-empty output for identical text across lists")
	}
}
