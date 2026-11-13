package docket

import (
	"errors"
	"strings"
	"testing"
)

type mockDiffProvider struct {
	files []string
	err   error
}

func (m *mockDiffProvider) ChangedFiles() ([]string, error) {
	return m.files, m.err
}

func TestGreenWithSourceChange(t *testing.T) {
	p := &mockDiffProvider{files: []string{"internal/pbd/docket/x.go"}}
	err := CheckGreen("internal/pbd/docket", p)
	if err != nil {
		t.Fatalf("expected allowed, got error: %v", err)
	}
}

func TestBlockGreenWithoutSourceChange(t *testing.T) {
	p := &mockDiffProvider{files: []string{"README.md"}}
	err := CheckGreen("internal/pbd/docket", p)
	if err == nil {
		t.Fatalf("expected error M1771, got nil")
	}
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != "M1771" {
		t.Fatalf("expected CodeError with code M1771, got %v", err)
	}
	if !strings.Contains(err.Error(), "M1771") {
		t.Fatalf("expected error message to contain M1771, got %v", err)
	}
}

func TestBlockGreenWithOutOfScopeChange(t *testing.T) {
	p := &mockDiffProvider{files: []string{"internal/other/y.go"}}
	err := CheckGreen("internal/pbd/docket", p)
	if err == nil {
		t.Fatalf("expected error M1771, got nil")
	}
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != "M1771" {
		t.Fatalf("expected CodeError with code M1771, got %v", err)
	}
}

func TestBlockGreenWithEmptyDiff(t *testing.T) {
	p := &mockDiffProvider{files: []string{}}
	err := CheckGreen("internal/pbd/docket", p)
	if err == nil {
		t.Fatalf("expected error M1771, got nil")
	}
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != "M1771" {
		t.Fatalf("expected CodeError with code M1771, got %v", err)
	}
}
