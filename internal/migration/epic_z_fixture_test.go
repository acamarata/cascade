// Purpose: verify immutable fixture provenance and materialize temporary derivatives.
// Inputs: the dot-free fixture, README checksums, and committed source inputs.
// Outputs: independently recounted domains and isolated source homes.
// Constraints: only synthetic vault names; substitutions occur only in temporary files.
// SPORT: migration fixture acceptance.
package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
)

const epicFixture = "testdata/v1-home"

func fixtureRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureHashes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(epicFixture, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			out[filepath.ToSlash(path)] = fmt.Sprintf("%x", sha256.Sum256(fixtureRead(t, path)))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("empty fixture")
	}
	return out
}

func renamedFixtureVault(data []byte) []byte {
	n := 0
	return regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*=`).ReplaceAllFunc(data, func([]byte) []byte {
		n++
		return fmt.Appendf(nil, "MIG_FIXTURE_SECRET_%d=", n)
	})
}

func TestFixtureDirMatchesInputs(t *testing.T) {
	rows := regexp.MustCompile("(?m)^`([^`]+)` \\| `([^`]+)` \\| `([a-f0-9]{64})`$").FindAllSubmatch(fixtureRead(t, epicFixture+"/README.md"), -1)
	if len(rows) == 0 {
		t.Fatal("README has no checksum rows")
	}
	listed := map[string]bool{}
	for _, row := range rows {
		path, source := string(row[1]), string(row[2])
		data := fixtureRead(t, filepath.Join(epicFixture, path))
		if listed[path] {
			t.Fatalf("duplicate README path: %s", path)
		}
		listed[path] = true
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != string(row[3]) {
			t.Errorf("checksum mismatch: %s", path)
		}
		if source == "derived" {
			continue
		}
		original := fixtureRead(t, filepath.Join("../..", source))
		if path == "vault.env" {
			original = renamedFixtureVault(original)
		}
		if !bytes.Equal(data, original) {
			t.Errorf("source mismatch: %s", path)
		}
	}
	for path := range fixtureHashes(t) {
		rel := strings.TrimPrefix(path, epicFixture+"/")
		if rel != "README.md" && !listed[rel] {
			t.Errorf("unlisted fixture: %s", rel)
		}
	}
}

func fixtureManifest(t *testing.T) map[string]int {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(fixtureRead(t, epicFixture+"/manifest.json"), &raw); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for domain, data := range raw {
		if domain == "config" {
			counts[domain] = fixtureConfigCounts(t)["planned_changes"]
			continue
		}
		var count int
		if err := json.Unmarshal(data, &count); err != nil {
			t.Fatal(err)
		}
		counts[domain] = count
	}
	return counts
}

func fixtureConfigCounts(t *testing.T) map[string]int {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(fixtureRead(t, epicFixture+"/manifest.json"), &raw); err != nil {
		t.Fatal(err)
	}
	var counts map[string]int
	if err := json.Unmarshal(raw["config"], &counts); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestFixtureManifestMatchesFiles(t *testing.T) {
	memory, err := filepath.Glob(epicFixture + "/dot-cascade/memory/*.md")
	if err != nil {
		t.Fatal(err)
	}
	var accounts struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(fixtureRead(t, epicFixture+"/dot-cascade/accounts/accounts.json"), &accounts); err != nil {
		t.Fatal(err)
	}
	assignments := regexp.MustCompile(`(?m)^[A-Za-z_][A-Za-z0-9_]*\s*=`)
	got := map[string]int{
		"memory": len(memory), "accounts": len(accounts.Accounts),
		"vault":  len(assignments.FindAll(fixtureRead(t, epicFixture+"/vault.env"), -1)),
		"config": len(assignments.FindAll(fixtureRead(t, epicFixture+"/dot-cascade/config.toml"), -1)),
	}
	want := fixtureManifest(t)
	want["config"] = fixtureConfigCounts(t)["source_keys"]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recount=%v manifest=%v", got, want)
	}
	assertFixtureConfigPlan(t, got["config"])
	for domain, count := range got {
		if count == 0 {
			t.Fatalf("empty domain: %s", domain)
		}
	}
}

func assertFixtureConfigPlan(t *testing.T, sourceKeys int) {
	t.Helper()
	counts := fixtureConfigCounts(t)
	result, err := migrationv1.NewConfigImporter(filepath.Join(t.TempDir(), "config.toml")).Import(t.Context(), migrationv1.Request{
		SourceRoot: materializeV1Home(t, true), DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counts["planned_changes"] != result.DeltaCount() || counts["planned_changes"]-sourceKeys != 1 {
		t.Fatalf("config plan=%+v counts=%v source keys=%d", result, counts, sourceKeys)
	}
	schema, source := 0, 0
	for _, change := range result.Changes {
		switch change.Source {
		case ".cascade/config.toml#schema_version":
			if change.Target != "schema_version" {
				t.Fatal("schema version mapped to a different key")
			}
			schema++
		case ".cascade/config.toml#daemon.log_level":
			source++
		default:
			t.Fatalf("unexpected config change: %+v", change)
		}
	}
	if schema != 1 || source != sourceKeys {
		t.Fatalf("extra config change is not exactly schema_version: %+v", result.Changes)
	}
}

func TestFixtureVaultNamesSynthetic(t *testing.T) {
	keys := regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)=`)
	original := keys.FindAllSubmatch(fixtureRead(t, "v1/testdata/v1-goldens/vault/vault.env"), -1)
	names := keys.FindAllSubmatch(fixtureRead(t, epicFixture+"/vault.env"), -1)
	if len(names) == 0 || len(names) != len(original) {
		t.Fatal("vault names missing")
	}
	for _, name := range names {
		if !regexp.MustCompile(`^MIG_FIXTURE_SECRET_[0-9]+$`).Match(name[1]) {
			t.Fatal("non-synthetic name")
		}
		for _, old := range original {
			if bytes.Equal(name[1], old[1]) {
				t.Fatal("original vault name survived")
			}
		}
	}
}

func materializeV1Home(t *testing.T, raw bool) string {
	t.Helper()
	root := t.TempDir()
	err := filepath.WalkDir(epicFixture, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(epicFixture, path)
		if err != nil {
			return err
		}
		if rel == "README.md" || rel == "manifest.json" {
			return nil
		}
		data := fixtureRead(t, path)
		if !raw {
			data = fixtureSubstitute(t, filepath.ToSlash(rel), data)
		}
		dest := strings.Replace(rel, "dot-cascade", ".cascade", 1)
		if rel == "vault.env" {
			dest = filepath.Join(".cascade", rel)
		}
		fixtureWrite(t, filepath.Join(root, dest), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func fixtureSubstitute(t *testing.T, rel string, data []byte) []byte {
	t.Helper()
	n := 0
	slug := regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(rel, "-")
	return regexp.MustCompile("REDACTED").ReplaceAllFunc(data, func([]byte) []byte {
		n++
		value := fmt.Sprintf("NONSECRET-%s-%d", slug, n)
		t.Logf("substitution %s occurrence %d -> %s", rel, n, value)
		return []byte(value)
	})
}
