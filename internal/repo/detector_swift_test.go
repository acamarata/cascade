package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSwiftDetectorPackageSwift(t *testing.T) {
	facts, err := (swiftDetector{}).Detect(context.Background(), "testdata/fixture-swift")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected || facts.Language != LanguageSwift {
		t.Fatalf("facts = %+v, want Detected=true Language=swift", facts)
	}
	if len(facts.Evidence) != 1 || facts.Evidence[0] != "Package.swift" {
		t.Errorf("Evidence = %v, want [Package.swift]", facts.Evidence)
	}
}

func TestSwiftDetectorXcodeproj(t *testing.T) {
	facts, err := (swiftDetector{}).Detect(context.Background(), "testdata/fixture-swift-xcode")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !facts.Detected {
		t.Fatal("Detected = false for a tree with App.xcodeproj/project.pbxproj")
	}
	if facts.Evidence[0] != "App.xcodeproj/project.pbxproj" {
		t.Errorf("Evidence = %v, want App.xcodeproj/project.pbxproj", facts.Evidence)
	}
}

func TestSwiftDetectorAbsent(t *testing.T) {
	facts, err := (swiftDetector{}).Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if facts.Detected {
		t.Error("Detected = true for a tree with no swift markers")
	}
}

func TestSwiftDetectorMalformedPackageSwift(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Package.swift"), []byte("// not a real manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (swiftDetector{}).Detect(context.Background(), dir); err == nil {
		t.Fatal("Detect: want error for Package.swift missing swift-tools-version, got nil")
	}
}

func TestSwiftDetectorMalformedPbxproj(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "App.xcodeproj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "project.pbxproj"), []byte("not a pbxproj"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (swiftDetector{}).Detect(context.Background(), dir); err == nil {
		t.Fatal("Detect: want error for project.pbxproj missing format marker, got nil")
	}
}
