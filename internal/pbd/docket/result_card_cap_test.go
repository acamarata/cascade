package docket

import (
	"strings"
	"testing"
)

// TestResultCardUnderCap verifies small input and exact-4096 output have no marker,
// and nil Decls emits an empty decls header.
func TestResultCardUnderCap(t *testing.T) {
	// Small input: should have no truncation marker.
	c := CommitInfo{
		Files: []string{"a.go", "b.go"},
		Decls: []string{"func Foo()"},
	}
	out, err := BuildResultCard(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "[truncated]") {
		t.Fatal("small input should not contain truncation marker")
	}
	if len(out) > Cap {
		t.Fatalf("output exceeds cap: %d", len(out))
	}

	// Nil Decls should still emit the decls header with no entries.
	cNil := CommitInfo{
		Files: []string{"x.go"},
		Decls: nil,
	}
	outNil, err := BuildResultCard(cNil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(outNil, "decls:\n") {
		t.Fatal("nil Decls should produce decls header")
	}
	if strings.Contains(outNil, "[truncated]") {
		t.Fatal("nil Decls card should not be truncated")
	}

	// Exactly 4096 bytes: build a card that hits the cap precisely.
	// "files:\n" = 7 bytes, "decls:\n" = 7 bytes => 14 bytes of headers.
	// Each file entry is "- " + name + "\n". Use one file to fill exactly.
	// We need total = 4096. 4096 - 14 = 4082 for entries.
	// One entry: 2 + len(name) + 1 = len(name) + 3. So len(name) = 4079.
	name := strings.Repeat("a", 4079)
	cExact := CommitInfo{
		Files: []string{name},
		Decls: nil,
	}
	outExact, err := BuildResultCard(cExact)
	if err != nil {
		t.Fatalf("unexpected error for exact cap: %v", err)
	}
	if len(outExact) != Cap {
		t.Fatalf("expected exactly %d bytes, got %d", Cap, len(outExact))
	}
	if strings.Contains(outExact, "[truncated]") {
		t.Fatal("exact-cap card should not have truncation marker")
	}
}

// TestResultCardTruncatesOverCap verifies truncation behaviour when output exceeds 4096 bytes.
func TestResultCardTruncatesOverCap(t *testing.T) {
	// 500 distinct 20-byte filenames.
	files := make([]string, 500)
	for i := range files {
		files[i] = strings.Repeat("f", 12) + padInt(i, 8)
	}

	c := CommitInfo{Files: files, Decls: nil}
	out, err := BuildResultCard(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) > Cap {
		t.Fatalf("output exceeds cap: %d > %d", len(out), Cap)
	}
	if !strings.HasSuffix(out, "[truncated]\n") {
		t.Fatal("over-cap output must end with [truncated]\\n")
	}

	// Decls are removed before Files.
	cDeclFirst := CommitInfo{
		Files: []string{"a.go"},
		Decls: []string{
			strings.Repeat("d", 4100),
			strings.Repeat("e", 4100),
		},
	}
	outDF, err := BuildResultCard(cDeclFirst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(outDF) > Cap {
		t.Fatalf("decl-first output exceeds cap: %d", len(outDF))
	}
	// All decls should be dropped before any file is dropped.
	if strings.Contains(outDF, "- "+strings.Repeat("d", 2000)) {
		t.Fatal("decls should be removed before files")
	}
	if !strings.Contains(outDF, "- a.go") {
		t.Fatal("file should remain after decls are removed")
	}

	// Single oversized filename leaves only headers and marker.
	cBig := CommitInfo{
		Files: []string{strings.Repeat("x", 5000)},
		Decls: nil,
	}
	outBig, err := BuildResultCard(cBig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "files:\ndecls:\n[truncated]\n"
	if outBig != expected {
		t.Fatalf("oversized single file: got %q, want %q", outBig, expected)
	}
}

func padInt(n, width int) string {
	s := strings.Repeat("0", width)
	ns := strings.TrimLeft(strings.Replace(s, s, intToStr(n), 1), "")
	if ns == "" {
		ns = "0"
	}
	if len(ns) < width {
		return strings.Repeat("0", width-len(ns)) + ns
	}
	return ns[:width]
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
