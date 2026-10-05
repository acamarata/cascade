package main

// Purpose: end-to-end harvest behaviour over a scratch module: canonical,
// content-addressed and idempotent output; redaction before every write;
// no private path in any byte; all-or-nothing writes on every failure.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHarvestCanonicalAndIdempotent(t *testing.T) {
	root := newModule(t)
	code, stdout, stderr := runHarvest(t)
	if code != exitOK || !strings.Contains(stdout, "8 written") {
		t.Fatalf("first run: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	first := readOutputs(t, root)
	assertCanonicalFixtures(t, first)
	for _, domain := range inputDomains {
		if _, ok := first[outputDirs[domain]+"/"+readmeName]; !ok {
			t.Errorf("%s has no README", domain)
		}
	}
	code, stdout, _ = runHarvest(t)
	if code != exitOK || !strings.Contains(stdout, " 0 written, 8 unchanged, 0 README(s) written") {
		t.Fatalf("second run: code %d stdout %q", code, stdout)
	}
	second := readOutputs(t, root)
	if len(second) != len(first) {
		t.Fatalf("second run changed the file set: %d -> %d", len(first), len(second))
	}
	for path, before := range first {
		after := second[path]
		if !bytes.Equal(before.data, after.data) || !before.mtime.Equal(after.mtime) {
			t.Errorf("%s changed on a repeat run", path)
		}
	}
	readme := string(second[outputDirs[v1.DomainMemory]+"/"+readmeName].data)
	if got := strings.Count(readme, "\n| memory | "); got != 4 {
		t.Errorf("memory README holds %d provenance rows, want 4", got)
	}
}

// assertCanonicalFixtures proves every fixture is canonical JSON named by
// the first 16 hex of its own sha256.
func assertCanonicalFixtures(t *testing.T, files map[string]outputFile) {
	t.Helper()
	fixtures := 0
	for path, file := range files {
		name := filepath.Base(path)
		if name == readmeName {
			continue
		}
		fixtures++
		var decoded map[string]any
		if err := json.Unmarshal(file.data, &decoded); err != nil {
			t.Fatalf("%s is not JSON: %v", path, err)
		}
		again, err := canonicalJSON(decoded)
		if err != nil || !bytes.Equal(again, file.data) {
			t.Errorf("%s is not canonical JSON", path)
		}
		sum := sha256.Sum256(file.data)
		if !strings.HasSuffix(name, "-"+hex.EncodeToString(sum[:])[:16]+".json") {
			t.Errorf("%s is not named by its content", path)
		}
	}
	if fixtures != 8 {
		t.Errorf("found %d fixtures, want 8", fixtures)
	}
}

func TestHarvestRedactsBeforeWrite(t *testing.T) {
	root := newModule(t)
	var seen [][]byte
	prev := writeFile
	writeFile = func(path string, data []byte) error {
		seen = append(seen, append([]byte(nil), data...))
		return prev(path, data)
	}
	t.Cleanup(func() { writeFile = prev })
	if code, _, stderr := runHarvest(t); code != exitOK {
		t.Fatalf("harvest failed: %s", stderr)
	}
	if len(seen) != 12 {
		t.Fatalf("writer saw %d writes, want 12 (8 fixtures + 4 READMEs)", len(seen))
	}
	forbidden := []string{fixtureSecretPrefix, "NONSECRET-", "example-acc1", "v1.account.", scratchParent, os.Getenv("HOME")}
	forbidden = append(forbidden, committedVaultNames(t, root)...)
	for i, data := range seen {
		for _, needle := range forbidden {
			if needle != "" && bytes.Contains(data, []byte(needle)) {
				t.Errorf("write %d carries an un-redacted value", i)
			}
		}
	}
}

// committedVaultNames parses the committed vault input's entry names. The
// names are compared, never printed.
func committedVaultNames(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pinnedOf(root), "vault", "vault.env"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := secrets.ParseVaultEnv(data)
	if err != nil || len(entries) == 0 {
		t.Fatalf("committed vault input does not parse to entries")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func TestHarvest_NoPrivatePathsInOutput(t *testing.T) {
	root := newModule(t)
	if code, _, stderr := runHarvest(t); code != exitOK {
		t.Fatalf("harvest failed: %s", stderr)
	}
	needles := []string{scratchParent, os.Getenv("HOME"), "/Users/", "/home/"}
	// The user name is matched in the forms it takes inside a path or as a
	// whole JSON value: a bare substring would fail on a CI user named after
	// an ordinary word that a committed memory body uses ("runner").
	if user := os.Getenv("USER"); user != "" {
		needles = append(needles, "/"+user+"/", `\`+user+`\`, `"`+user+`"`)
	}
	for path, file := range readOutputs(t, root) {
		for _, needle := range needles {
			if needle != "" && bytes.Contains(file.data, []byte(needle)) {
				t.Errorf("%s carries a private path or user name", path)
			}
		}
	}
}

func TestHarvest_AtomicNoPartialWrite(t *testing.T) {
	root := newModule(t)
	const failAt = 3
	calls := 0
	prev := writeFile
	writeFile = func(path string, data []byte) error {
		calls++
		if calls == failAt {
			return cascade.New(cascade.KindUnavailable, "injected write failure")
		}
		return prev(path, data)
	}
	t.Cleanup(func() { writeFile = prev })
	if code, _, _ := runHarvest(t); code != exitRefused {
		t.Fatalf("exit %d, want %d", code, exitRefused)
	}
	got := readOutputs(t, root)
	if len(got) != failAt-1 {
		t.Fatalf("%d files written, want %d (nothing after the failed write)", len(got), failAt-1)
	}
	for path := range got {
		if filepath.Base(path) == readmeName {
			t.Errorf("a README row was added despite the failed write: %s", path)
		}
	}
	if calls != failAt {
		t.Errorf("writer called %d times, want %d", calls, failAt)
	}
}

func TestHarvest_MissingSourceDirExits2(t *testing.T) {
	root := newModule(t)
	if err := os.RemoveAll(filepath.Join(pinnedOf(root), "accounts")); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runHarvest(t)
	if code != exitRefused || !strings.Contains(stderr, "pinned input dir accounts is absent") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if got := readOutputs(t, root); len(got) != 0 {
		t.Fatalf("%d files written after a refusal", len(got))
	}
}

func TestHarvest_ImporterErrorNoWrite(t *testing.T) {
	root := newModule(t)
	injected := cascade.New(cascade.KindUnavailable, "injected importer failure")
	prev := importerFor
	importerFor = func(domain v1.Domain, deps importerDeps) v1.Importer {
		if domain == v1.DomainAccounts {
			return failingImporter{err: injected}
		}
		return prev(domain, deps)
	}
	t.Cleanup(func() { importerFor = prev })
	code, _, stderr := runHarvest(t)
	if code != exitRefused || !strings.Contains(stderr, "the accounts importer refused accounts/accounts.json (unavailable)") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if strings.Contains(stderr, "injected importer failure") {
		t.Error("the importer's own message reached the output")
	}
	if got := readOutputs(t, root); len(got) != 0 {
		t.Fatalf("%d files written after an importer error", len(got))
	}
}
