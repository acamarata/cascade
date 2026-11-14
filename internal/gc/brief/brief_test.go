package brief

import (
	"strings"
	"testing"
)

func TestUnder8KBAccepted(t *testing.T) {
	// 8191-byte loop brief with 1 package and 2 files is accepted
	text := strings.Repeat("a", 8191)
	b := Brief{
		Text:  text,
		Scope: []string{"scope1"},
		Files: []string{"internal/a/x.go", "internal/a/y.go"},
	}
	err := Validate("loop", b)
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestOver8KBRejected(t *testing.T) {
	// 8192-byte brief returns ErrTooLarge
	text := strings.Repeat("a", 8192)
	b := Brief{
		Text:  text,
		Scope: []string{"scope1"},
		Files: []string{"internal/a/x.go"},
	}
	err := Validate("loop", b)
	if err != ErrTooLarge {
		t.Errorf("expected ErrTooLarge, got %v", err)
	}
}

func TestSevenScopeEntriesRejected(t *testing.T) {
	// 7 scope entries returns ErrTooManyScope
	b := Brief{
		Text:  "short",
		Scope: []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7"},
		Files: []string{"internal/a/x.go"},
	}
	err := Validate("loop", b)
	if err != ErrTooManyScope {
		t.Errorf("expected ErrTooManyScope, got %v", err)
	}
}

func TestMultiPackageRejected(t *testing.T) {
	// files in internal/a and internal/b return ErrMultiPackage
	b := Brief{
		Text:  "short",
		Scope: []string{"scope1"},
		Files: []string{"internal/a/x.go", "internal/b/y.go"},
	}
	err := Validate("loop", b)
	if err != ErrMultiPackage {
		t.Errorf("expected ErrMultiPackage, got %v", err)
	}
}

func TestMissingFileListRejected(t *testing.T) {
	// loop brief with a package and empty file list returns ErrNoFileList
	b := Brief{
		Text:  "short",
		Scope: []string{"scope1"},
		Files: []string{},
	}
	err := Validate("loop", b)
	if err != ErrNoFileList {
		t.Errorf("expected ErrNoFileList, got %v", err)
	}
}
