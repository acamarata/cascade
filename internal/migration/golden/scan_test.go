package main

// Purpose: TestHarvestedFixturesPassSecretScan runs the internal/secrets
// detector directly (the tree gate skips testdata) over every committed
// file in the four migration/ dirs, and proves the scan is live by planting
// a detector-shaped value in a scratch copy. Also covers the output guard.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
)

// moduleRelRoot is the module root relative to this package.
const moduleRelRoot = "../../.."

// plantedCredential is a URL-embedded password, split so this file does not
// itself read as a credential to a line scanner (C22).
var plantedCredential = "postgres://fixture:" + "Zq8vK2mW9xLp4RtY" + "@db.example.invalid:5432/app"

func TestHarvestedFixturesPassSecretScan(t *testing.T) {
	det, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	for _, domain := range inputDomains {
		dir := filepath.Join(moduleRelRoot, filepath.FromSlash(outputDirs[domain]))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("committed %s fixtures are missing: %v", domain, err)
		}
		fixtures := 0
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if fixtureNamePattern.MatchString(entry.Name()) {
				fixtures++
			}
			if hits := det.ScanCertain(data); len(hits) != 0 {
				t.Errorf("%s/%s: %d detector finding(s)", outputDirs[domain], entry.Name(), len(hits))
			}
			planted := filepath.Join(scratch, string(domain)+"-"+entry.Name())
			writeTestFile(t, planted, append(append([]byte(nil), data...), []byte(plantedCredential+"\n")...))
			copyData, err := os.ReadFile(planted)
			if err != nil {
				t.Fatal(err)
			}
			if len(det.ScanCertain(copyData)) == 0 {
				t.Errorf("a planted credential in a copy of %s went undetected", entry.Name())
			}
		}
		if fixtures == 0 {
			t.Errorf("%s holds no committed fixture", outputDirs[domain])
		}
	}
}

func TestCommittedFixturesCarryNoPrivatePath(t *testing.T) {
	for _, domain := range inputDomains {
		dir := filepath.Join(moduleRelRoot, filepath.FromSlash(outputDirs[domain]))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("committed %s fixtures are missing: %v", domain, err)
		}
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, needle := range []string{"/Users/", "/home/", fixtureSecretPrefix, "NONSECRET-", "cascade-golden-"} {
				if strings.Contains(string(data), needle) {
					t.Errorf("%s/%s carries a private path or synthetic name", outputDirs[domain], entry.Name())
				}
			}
		}
	}
}

func TestGuardBytes(t *testing.T) {
	det, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := guardBytes(det, "ok.json", []byte(`{"a":"REDACTED"}`)); err != nil {
		t.Fatalf("clean bytes refused: %v", err)
	}
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	refused := map[string]string{
		"credential": plantedCredential,
		"users":      `{"p":"/Users/x/y"}`,
		"synthetic":  `{"t":"` + fixtureSecretPrefix + `1"}`,
		"nonsecret":  `{"v":"NONSECRET-vault-1"}`,
		"home":       os.Getenv("HOME") + "/x",
	}
	for name, text := range refused {
		err := guardBytes(det, "f.json", []byte(text))
		if err == nil || !strings.Contains(err.Error(), "redaction failure: f.json") {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), text) {
			t.Errorf("%s: the refusal quotes the matched bytes", name)
		}
	}
	if err := guardFixtures([]fixture{{Name: "x.json", Data: []byte(plantedCredential)}}); err == nil {
		t.Error("guardFixtures passed a credential")
	}
}
