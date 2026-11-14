package docket

import (
	"testing"
)

// TestResultCardListsFilesAndDecls verifies that BuildResultCard renders
// files and declarations with proper headers and list formatting.
func TestResultCardListsFilesAndDecls(t *testing.T) {
	info := CommitInfo{
		Files: []string{"a.go", "b.go"},
		Decls: []string{"func Foo"},
	}

	got, err := BuildResultCard(info)
	if err != nil {
		t.Fatalf("BuildResultCard returned error: %v", err)
	}

	want := "files:\n- a.go\n- b.go\ndecls:\n- func Foo\n"
	if got != want {
		t.Errorf("BuildResultCard output mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}
