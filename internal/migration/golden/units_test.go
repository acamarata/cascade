package main

// Purpose: unit coverage for the small refusal paths the end-to-end tests
// do not reach: malformed v2 memory read-back, unsafe targets, scratch
// staging failures and the importer-refusal error chain.

import (
	"errors"
	"path/filepath"
	"testing"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseV2Memory(t *testing.T) {
	fm, body, err := parseV2Memory([]byte("---\nname: \"x\"\nconfidence: 1\n---\nbody\n"))
	if err != nil || fm["name"] != "x" || fm["confidence"] != "1" || body != "body\n" {
		t.Fatalf("parse = %v %q %v", fm, body, err)
	}
	for name, text := range map[string]string{
		"no-fence":     "name: x\n",
		"unclosed":     "---\nname: x\n",
		"no-separator": "---\nname\n---\n",
		"bad-quote":    "---\nname: \"x\n---\n",
	} {
		if _, _, err := parseV2Memory([]byte(text)); err == nil {
			t.Errorf("%s: malformed v2 memory accepted", name)
		}
	}
}

func TestSafeMemoryTarget(t *testing.T) {
	for target, want := range map[string]bool{
		"project/decisions": true,
		"decisions":         false,
		"nokind/decisions":  false,
		"project/../x":      false,
	} {
		if got := safeMemoryTarget(target); got != want {
			t.Errorf("safeMemoryTarget(%q) = %v, want %v", target, got, want)
		}
	}
}

func TestMemoryFixture_Refusals(t *testing.T) {
	file := inputFile{Domain: v1.DomainMemory, Rel: "memory/x.md"}
	none := v1.DryRunResult{}
	if _, err := memoryFixture(t.TempDir(), file, none); err == nil {
		t.Error("an input with no Change was accepted")
	}
	unsafe := v1.DryRunResult{Changes: []v1.Change{{Operation: v1.OperationCreate, Source: ".cascade/memory/x.md", Target: "../x"}}}
	if _, err := memoryFixture(t.TempDir(), file, unsafe); err == nil {
		t.Error("an unsafe target was accepted")
	}
	missing := v1.DryRunResult{Changes: []v1.Change{{Operation: v1.OperationCreate, Source: ".cascade/memory/x.md", Target: "project/x"}}}
	if _, err := memoryFixture(t.TempDir(), file, missing); err == nil {
		t.Error("a missing v2 file was accepted")
	}
	store := t.TempDir()
	writeTestFile(t, filepath.Join(store, "project", "x.md"), []byte("not frontmatter"))
	if _, err := memoryFixture(store, file, missing); err == nil {
		t.Error("a malformed v2 file was accepted")
	}
}

func TestImportRefusal_Chain(t *testing.T) {
	cause := cascade.New(cascade.KindConflict, "inner detail")
	err := error(&importRefusal{domain: v1.DomainVault, input: "vault/x.env", cause: cause})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce != cause {
		t.Fatal("the importer error is not reachable through Unwrap")
	}
	if got := err.Error(); got != "golden harvest: the vault importer refused vault/x.env (conflict)" {
		t.Errorf("Error() = %q", got)
	}
	if _, err := runImporter(t.Context(), nil, v1.DomainVault, "x", t.TempDir()); err == nil {
		t.Error("a nil importer was accepted")
	}
}

func TestScratchStagingFailures(t *testing.T) {
	sealEnv(t)
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "file"), []byte("x"))
	if err := stage(root, "file/below.txt", []byte("x")); err == nil {
		t.Error("staging under a file was accepted")
	}
	if err := stage(root, "a/b.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := stage(root, "a/b.txt", []byte("x")); err == nil {
		t.Error("restaging over an existing scratch file was accepted")
	}
	scratchParent = filepath.Join(root, "absent", "deeper")
	if _, err := newScratch(); err == nil {
		t.Error("a scratch dir under a missing parent was created")
	}
	if _, err := harvestMemory(t.Context(), clockFor(), []inputFile{{Rel: "memory/x.md"}}); err == nil {
		t.Error("memory harvest without scratch succeeded")
	}
}

func TestRun_RealWriterRefusesBadFlag(t *testing.T) {
	sealEnv(t)
	if code := Run([]string{"--bogus"}); code != exitRefused {
		t.Errorf("Run exit %d, want %d", code, exitRefused)
	}
}
