package crunch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadMarkerValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marker.txt")
	content := []byte("valid marker content")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	got, err := ReadMarker(path)
	if err != nil {
		t.Fatalf("ReadMarker returned unexpected error: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("ReadMarker returned %q, want %q", got, content)
	}
}

func TestReadMarkerOversizedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marker.txt")

	// Create a file larger than MarkerSizeCap
	content := make([]byte, MarkerSizeCap+1)
	for i := range content {
		content[i] = 'a'
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	got, err := ReadMarker(path)
	if !errors.Is(err, ErrD1378) {
		t.Fatalf("ReadMarker returned error %v, want ErrD1378", err)
	}
	if got != nil {
		t.Errorf("ReadMarker returned %v, want nil", got)
	}
}

func TestReadMarkerNonRegularFile(t *testing.T) {
	dir := t.TempDir()

	// Test directory
	_, err := ReadMarker(dir)
	if !errors.Is(err, ErrD1378) {
		t.Errorf("ReadMarker on directory returned error %v, want ErrD1378", err)
	}

	// Test symlink
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	got, err := ReadMarker(symlink)
	if !errors.Is(err, ErrD1378) {
		t.Errorf("ReadMarker on symlink returned error %v, want ErrD1378", err)
	}
	if got != nil {
		t.Errorf("ReadMarker on symlink returned %v, want nil", got)
	}
}

func TestReadMarkerEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("failed to write empty test file: %v", err)
	}

	got, err := ReadMarker(path)
	if err != nil {
		t.Fatalf("ReadMarker returned unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadMarker returned %d bytes, want 0", len(got))
	}
}
