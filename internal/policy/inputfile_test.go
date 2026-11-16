package policy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSymlinkPrivateRepoIntoPublicRootRejected(t *testing.T) {
	tmpDir := t.TempDir()
	canonicalTmp, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("failed to canonicalize tmp dir: %v", err)
	}

	// Create a synthetic private repo outside public root
	privateRepo := filepath.Join(canonicalTmp, "private-repo")
	if err := os.MkdirAll(privateRepo, 0755); err != nil {
		t.Fatalf("failed to create private repo: %v", err)
	}
	privateFile := filepath.Join(privateRepo, "secret.txt")
	if err := os.WriteFile(privateFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("failed to create private file: %v", err)
	}

	// Create a public root
	publicRoot := filepath.Join(canonicalTmp, "public-root")
	if err := os.MkdirAll(publicRoot, 0755); err != nil {
		t.Fatalf("failed to create public root: %v", err)
	}

	tests := []struct {
		name    string
		setup   func() string // returns the path to check
		wantErr bool
	}{
		{
			name: "symlink in public root pointing to private repo",
			setup: func() string {
				linkPath := filepath.Join(publicRoot, "link-to-private")
				if err := os.Symlink(privateRepo, linkPath); err != nil {
					t.Fatalf("failed to create symlink: %v", err)
				}
				return filepath.Join(linkPath, "secret.txt")
			},
			wantErr: true,
		},
		{
			name: "symlink ancestor in path",
			setup: func() string {
				subdir := filepath.Join(publicRoot, "subdir")
				if err := os.MkdirAll(subdir, 0755); err != nil {
					t.Fatalf("failed to create subdir: %v", err)
				}
				linkPath := filepath.Join(subdir, "link-to-private")
				if err := os.Symlink(privateRepo, linkPath); err != nil {
					t.Fatalf("failed to create symlink: %v", err)
				}
				return filepath.Join(linkPath, "secret.txt")
			},
			wantErr: true,
		},
		{
			name: "symlink outside root pointing to private repo",
			setup: func() string {
				outsideDir := filepath.Join(canonicalTmp, "outside")
				if err := os.MkdirAll(outsideDir, 0755); err != nil {
					t.Fatalf("failed to create outside dir: %v", err)
				}
				linkPath := filepath.Join(outsideDir, "link-to-private")
				if err := os.Symlink(privateRepo, linkPath); err != nil {
					t.Fatalf("failed to create symlink: %v", err)
				}
				return filepath.Join(linkPath, "secret.txt")
			},
			wantErr: true,
		},
		{
			name: "dangling symlink in public root",
			setup: func() string {
				linkPath := filepath.Join(publicRoot, "dangling-link")
				target := filepath.Join(canonicalTmp, "nonexistent", "file.txt")
				if err := os.Symlink(target, linkPath); err != nil {
					t.Fatalf("failed to create dangling symlink: %v", err)
				}
				return linkPath
			},
			wantErr: true,
		},
		{
			name: "valid file under public root",
			setup: func() string {
				validFile := filepath.Join(publicRoot, "valid.txt")
				if err := os.WriteFile(validFile, []byte("ok"), 0644); err != nil {
					t.Fatalf("failed to create valid file: %v", err)
				}
				return validFile
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup()
			err := CheckInputFile(path, []string{publicRoot})
			if tt.wantErr {
				if err == nil {
					t.Errorf("CheckInputFile(%q) = nil, want error", path)
				} else if !errors.Is(err, ErrUnsafeInputFile) {
					t.Errorf("CheckInputFile(%q) = %v, want ErrUnsafeInputFile", path, err)
				}
			} else {
				if err != nil {
					t.Errorf("CheckInputFile(%q) = %v, want nil", path, err)
				}
			}
		})
	}
}
