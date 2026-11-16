package atlas

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRebuildIndexFailsClosedOnInvalidPath(t *testing.T) {
	validDB := &sql.DB{}

	tmpDir := t.TempDir()
	symlinkPath := filepath.Join(tmpDir, "symlink")
	targetPath := filepath.Join(tmpDir, "target")
	if err := os.Mkdir(targetPath, 0755); err != nil {
		t.Fatalf("failed to create target dir: %v", err)
	}
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	filePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	tests := []struct {
		name string
		dir  string
		db   *sql.DB
	}{
		{"nil db", tmpDir, nil},
		{"empty dir", "", validDB},
		{"relative dir", "relative/dir", validDB},
		{"nonexistent dir", "/path/does/not/exist/anywhere/12345", validDB},
		{"symlink dir", symlinkPath, validDB},
		{"not a dir", filePath, validDB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RebuildIndex(tt.dir, tt.db)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrRebuildIndex) {
				t.Errorf("expected error to wrap ErrRebuildIndex, got %v", err)
			}
		})
	}
}
