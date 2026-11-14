package background

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintDenyShellCommand(t *testing.T) {
	root := t.TempDir()
	out, err := CheckPrint(PrintRequest{
		Folder:  root,
		Path:    "file.txt",
		Command: "echo hi",
	})
	if out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
	if !errors.Is(err, ErrShellDenied) {
		t.Fatalf("expected ErrShellDenied, got %v", err)
	}
	if !strings.Contains(err.Error(), "D3127") {
		t.Fatalf("expected error text D3127, got %q", err.Error())
	}
}

func TestPrintDenyOutsideRead_BasicCases(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name   string
		folder string
		path   string
	}{
		{"blank folder", "", "file.txt"},
		{"blank path", root, ""},
		{"missing root", filepath.Join(root, "missing"), "file.txt"},
		{"outside path", root, "/etc/passwd"},
		{"../ escape", root, "../file.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := CheckPrint(PrintRequest{Folder: tc.folder, Path: tc.path})
			if !errors.Is(err, ErrOutsideRead) || out != "" {
				t.Fatalf("%s failed", tc.name)
			}
		})
	}
}

func TestPrintDenyOutsideRead_SymlinkAndFileRoot(t *testing.T) {
	root := t.TempDir()

	// escaping symlink
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	symlinkPath := filepath.Join(root, "escape_link")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Fatalf("symlink failed: %v", err)
	}

	out, err := CheckPrint(PrintRequest{Folder: root, Path: "escape_link"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("escaping symlink failed")
	}

	// non-directory root
	fileRoot := filepath.Join(root, "afile.txt")
	if err := os.WriteFile(fileRoot, []byte("hi"), 0600); err != nil {
		t.Fatalf("write file root: %v", err)
	}
	out, err = CheckPrint(PrintRequest{Folder: fileRoot, Path: "file.txt"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("non-directory root failed")
	}
}
