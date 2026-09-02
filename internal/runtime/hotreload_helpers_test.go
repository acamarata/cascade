package runtime

import (
	"os"
	"testing"
)

// Purpose: tiny filesystem test helpers shared across
//   toml_edit_test.go/hotreload_test.go/baseline_test.go — every caller
//   passes a t.TempDir()-rooted path (Art.7.1).

// writeFile already exists (rotation_test.go); this file reuses it.

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile(%s): %v", path, err)
	}
	return string(data)
}

func appendComment(path, comment string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(comment)
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listDir(%s): %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
