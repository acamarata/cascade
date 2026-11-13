package forge

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestScanPinsFlagsChangedManifest(t *testing.T) {
	dir := t.TempDir()

	manifestPath := filepath.Join(dir, "manifest.yaml")
	fixturePath := filepath.Join(dir, "fixture.yaml")

	content := []byte("manifest content")
	if err := os.WriteFile(manifestPath, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, []byte("fixture content"), 0644); err != nil {
		t.Fatal(err)
	}

	actualSHA := fmt.Sprintf("%x", sha256.Sum256(content))
	wrongSHA := "0000000000000000000000000000000000000000000000000000000000000000"

	fixtures := []Fixture{
		{Path: fixturePath, SHA: wrongSHA},
	}

	violations, err := ScanPins([]string{manifestPath}, fixtures)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}

	v := violations[0]
	if v.ManifestPath != manifestPath {
		t.Errorf("expected ManifestPath %s, got %s", manifestPath, v.ManifestPath)
	}
	if v.FixturePath != fixturePath {
		t.Errorf("expected FixturePath %s, got %s", fixturePath, v.FixturePath)
	}
	if v.ExpectedSHA != wrongSHA {
		t.Errorf("expected ExpectedSHA %s, got %s", wrongSHA, v.ExpectedSHA)
	}
	if v.ActualSHA != actualSHA {
		t.Errorf("expected ActualSHA %s, got %s", actualSHA, v.ActualSHA)
	}

	// Test matching SHA yields no violations
	fixtures[0].SHA = actualSHA
	violations, err = ScanPins([]string{manifestPath}, fixtures)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("expected 0 violations for matching SHA, got %d", len(violations))
	}
}

func TestScanPinsUnreadableManifest(t *testing.T) {
	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(fixturePath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ScanPins([]string{filepath.Join(dir, "nonexistent.yaml")}, []Fixture{{Path: fixturePath, SHA: "abc"}})
	if err == nil {
		t.Error("expected error for unreadable manifest, got nil")
	}
}

func TestScanPinsUnreadableFixture(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ScanPins([]string{manifestPath}, []Fixture{{Path: filepath.Join(dir, "nonexistent.yaml"), SHA: "abc"}})
	if err == nil {
		t.Error("expected error for unreadable fixture, got nil")
	}
}
