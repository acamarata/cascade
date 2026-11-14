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

func TestPrintDenyOutsideRead(t *testing.T) {
	root := t.TempDir()

	// blank folder
	out, err := CheckPrint(PrintRequest{Folder: "", Path: "file.txt"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("blank folder failed")
	}

	// blank path
	out, err = CheckPrint(PrintRequest{Folder: root, Path: ""})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("blank path failed")
	}

	// missing root
	out, err = CheckPrint(PrintRequest{Folder: filepath.Join(root, "missing"), Path: "file.txt"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("missing root failed")
	}

	// outside path
	out, err = CheckPrint(PrintRequest{Folder: root, Path: "/etc/passwd"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("outside path failed")
	}

	// ../ escape
	out, err = CheckPrint(PrintRequest{Folder: root, Path: "../file.txt"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("../ escape failed")
	}

	// escaping symlink
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0600)

	symlinkPath := filepath.Join(root, "escape_link")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Fatalf("symlink failed: %v", err)
	}

	out, err = CheckPrint(PrintRequest{Folder: root, Path: "escape_link"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("escaping symlink failed")
	}

	// non-directory root
	fileRoot := filepath.Join(root, "afile.txt")
	os.WriteFile(fileRoot, []byte("hi"), 0600)
	out, err = CheckPrint(PrintRequest{Folder: fileRoot, Path: "file.txt"})
	if !errors.Is(err, ErrOutsideRead) || out != "" {
		t.Fatalf("non-directory root failed")
	}
}
