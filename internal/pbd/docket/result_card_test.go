package docket

import (
	"errors"
	"testing"
)

func TestResultCardEmptyCommitRejected(t *testing.T) {
	cases := []struct {
		name    string
		info    CommitInfo
		wantErr error
		wantMsg string // checked only when non-empty
	}{
		{"nil Files", CommitInfo{Files: nil, Decls: []string{"decl1"}}, ErrCardEmpty, "CARD_EMPTY"},
		{"empty Files", CommitInfo{Files: []string{}, Decls: []string{"decl1"}}, ErrCardEmpty, ""},
		{"empty entry", CommitInfo{Files: []string{""}, Decls: []string{}}, ErrCardInput, "CARD_INVALID"},
		{"duplicate entry", CommitInfo{Files: []string{"a", "a"}, Decls: []string{}}, ErrCardInput, ""},
		{"invalid UTF-8", CommitInfo{Files: []string{"\xff"}, Decls: []string{}}, ErrCardInput, ""},
		{"CR", CommitInfo{Files: []string{"a\rb"}, Decls: []string{}}, ErrCardInput, ""},
		{"LF", CommitInfo{Files: []string{"a\nb"}, Decls: []string{}}, ErrCardInput, ""},
		{"NUL", CommitInfo{Files: []string{"a\x00b"}, Decls: []string{}}, ErrCardInput, ""},
		{"duplicate in Decls", CommitInfo{Files: []string{"a"}, Decls: []string{"b", "b"}}, ErrCardInput, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Render(tc.info)
			if out != "" {
				t.Errorf("expected empty output for %s, got %q", tc.name, out)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v for %s, got %v", tc.wantErr, tc.name, err)
			}
			if tc.wantMsg != "" && err.Error() != tc.wantMsg {
				t.Errorf("expected Error() %s, got %q", tc.wantMsg, err.Error())
			}
		})
	}
}

func TestResultCardIdenticalTextAcrossListsPermitted(t *testing.T) {
	out, err := Render(CommitInfo{Files: []string{"same"}, Decls: []string{"same"}})
	if err != nil {
		t.Errorf("expected no error for identical text across lists, got %v", err)
	}
	if out == "" {
		t.Errorf("expected non-empty output for identical text across lists")
	}
}
