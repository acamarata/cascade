package repo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetectAllFallsThroughToGeneric(t *testing.T) {
	facts, err := DetectAll(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}
	if len(facts) != 1 || facts[0].Language != LanguageGeneric {
		t.Fatalf("facts = %+v, want exactly one generic entry", facts)
	}
}

func TestDetectAllMultipleFamilies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n")
	writeFile(t, dir, "package.json", `{"name":"x"}`)

	facts, err := DetectAll(context.Background(), dir)
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %+v, want exactly 2 (go + jsts)", facts)
	}
}

func TestDetectAllMalformedManifestIsFatal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "not { valid")
	if _, err := DetectAll(context.Background(), dir); err == nil {
		t.Fatal("DetectAll: want error for malformed go.mod, got nil (must never fall through to generic)")
	}
}

func TestDetectAllOverridesFromMakefile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n")
	writeFile(t, dir, "Makefile", "build:\n\techo hi\n")

	facts, err := DetectAll(context.Background(), dir)
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}
	if len(facts) != 1 || facts[0].Commands.Build != "make build" {
		t.Fatalf("facts = %+v, want Makefile build target to override the go default", facts)
	}
	if facts[0].Commands.Test != "go test ./..." {
		t.Errorf("Test = %q, want unoverridden go default (Makefile has no test target)", facts[0].Commands.Test)
	}
}

func TestDetectAllOverrideFixture(t *testing.T) {
	facts, err := DetectAll(context.Background(), "testdata/fixture-override")
	if err != nil {
		t.Fatalf("DetectAll: %v", err)
	}
	if len(facts) != 1 || facts[0].Language != LanguageGeneric {
		t.Fatalf("facts = %+v, want one generic entry (no language marker present)", facts)
	}
	if facts[0].Commands.Build != "make build" {
		t.Errorf("Build = %q, want make build", facts[0].Commands.Build)
	}
}

func TestDetectAllEmptyRootRefused(t *testing.T) {
	if _, err := DetectAll(context.Background(), ""); err == nil {
		t.Fatal("DetectAll(\"\"): want error, got nil")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceExistsPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := evidenceExists(dir, "go.mod"); err == nil {
		t.Fatal("evidenceExists: want error for an unreadable directory, got nil")
	}
}

func TestApplyOverridesSkipsGeneric(t *testing.T) {
	in := LanguageFacts{Language: LanguageGeneric, Commands: Commands{Build: "make build"}}
	out := applyOverrides(in, map[string]bool{"build": true, "test": true, "lint": true})
	if out.Commands != in.Commands {
		t.Errorf("applyOverrides rewrote a generic family's own commands: %+v", out.Commands)
	}
}

func TestApplyOverridesNoOverrides(t *testing.T) {
	in := LanguageFacts{Language: LanguageGo, Commands: Commands{Build: "go build ./..."}}
	if out := applyOverrides(in, nil); out.Commands != in.Commands {
		t.Errorf("applyOverrides changed commands with an empty override set: %+v", out.Commands)
	}
}

func TestMakefileOverridesReadError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on windows")
	}
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "build:\n\techo hi\n")
	if err := os.Chmod(filepath.Join(dir, "Makefile"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "Makefile"), 0o644) })
	if _, err := makefileOverrides(dir); err == nil {
		t.Fatal("makefileOverrides: want error for an unreadable Makefile, got nil")
	}
}

// FuzzDetectManifest drives every family's manifest-parsing function
// directly with arbitrary bytes: none may ever panic, and each must
// return either well-formed LanguageFacts or a typed error -- never a
// silently-guessed result.
func FuzzDetectManifest(f *testing.F) {
	f.Add([]byte("module example.com/x\n\ngo 1.22\n"))
	f.Add([]byte(`{"name":"x"}`))
	f.Add([]byte("[package]\nname = \"x\"\n"))
	f.Add([]byte("[project]\nname = \"x\"\n"))
	f.Add([]byte("// swift-tools-version:5.9\n"))
	f.Add([]byte("!$*UTF8*$!"))
	f.Add([]byte{})
	f.Add([]byte("\x00\x01\xff{[("))

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, parse := range []func([]byte) (LanguageFacts, error){
			parseGoMod, parsePackageJSON, parseCargoToml,
			parsePyprojectToml, parsePackageSwift, parsePbxproj,
		} {
			facts, err := parse(data)
			if err != nil {
				if facts.Language != "" || facts.Detected {
					t.Fatalf("non-zero facts alongside a non-nil error: %+v, %v", facts, err)
				}
				continue
			}
			if !facts.Language.Valid() {
				t.Fatalf("parse returned invalid Language %q with nil error", facts.Language)
			}
		}
	})
}
