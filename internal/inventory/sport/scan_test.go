// SPORT: internal.inventory.sport.ScanFiles/ADDED (tests).
package sport

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanFiles_MissingFileSkipped(t *testing.T) {
	dir := t.TempDir()
	entities, errs, err := ScanFiles(dir, []string{"does-not-exist.go"})
	if err != nil {
		t.Fatalf("expected missing file to be skipped, got error: %v", err)
	}
	if len(entities) != 0 || len(errs) != 0 {
		t.Fatalf("expected nothing, got entities=%v errs=%v", entities, errs)
	}
}

func TestScanFiles_ParsesRealFileShape(t *testing.T) {
	dir := t.TempDir()
	content := "package x\n\n// SPORT: pkg.x.Thing/ADDED (T-1).\nfunc Thing() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entities, errs, err := ScanFiles(dir, []string{"x.go"})
	if err != nil {
		t.Fatalf("ScanFiles: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}
	if len(entities) != 1 || entities[0].entity.Name != "pkg.x.Thing" || entities[0].line != 3 {
		t.Fatalf("got %+v", entities)
	}
}

func TestScanFiles_PropagatesMalformed(t *testing.T) {
	dir := t.TempDir()
	content := "package x\n\n// SPORT: CHANGED\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errs, err := ScanFiles(dir, []string{"x.go"})
	if err != nil {
		t.Fatalf("ScanFiles: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("want 1 malformed error, got %v", errs)
	}
}
