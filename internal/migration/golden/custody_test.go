package main

// Purpose: custody isolation. The harvest custody is the encrypted file
// vault under the dedicated service label, it never calls a platform
// command even when one would answer, it only ever holds synthetic names,
// and Run obtains its config only through custodyConfigFor.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
)

// syntheticName matches the only entry names a harvest custody may hold.
var syntheticName = regexp.MustCompile(`^MIG_FIXTURE_SECRET_[0-9]+$`)

func TestHarvestCustodyConfig_Fields(t *testing.T) {
	scratch := t.TempDir()
	cfg := harvestCustodyConfig(scratch)
	if cfg.Service != "cascade-golden-harvest" || !cfg.ForceFileVault {
		t.Fatalf("config = service %q force %v", cfg.Service, cfg.ForceFileVault)
	}
	if !within(scratch, cfg.Dir) || cfg.Dir == scratch {
		t.Fatalf("custody dir is not a subdir of scratch")
	}
	if cfg.Runner != nil || cfg.KeychainPath != "" || cfg.Passphrase != "" {
		t.Fatal("harvest config carries a platform or credential field")
	}
}

func TestHarvestCustody_NeverCallsPlatform(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the recording runner seam is the darwin keychain backend's")
	}
	sealEnv(t)
	rec := newRecordingRunner(t)
	scratch := t.TempDir()
	cfg := harvestCustodyConfig(scratch)
	cfg.Runner = rec.run
	custody, err := secrets.SelectCustody(cfg)
	assertNoPlatformCalls(t, rec)
	if err != nil {
		t.Fatal(err)
	}
	if custody.Name() != fileVaultBackend {
		t.Fatalf("custody backend %q, want the file vault", custody.Name())
	}
	if _, err := harvestVault(context.Background(), scratch, inputFor(t, "vault/vault.env"), cfg); err != nil {
		t.Fatalf("vault harvest: %v", err)
	}
	assertNoPlatformCalls(t, rec)
}

// assertNoPlatformCalls fails when the recording runner saw any platform
// command, naming the security verbs it saw (never an argument value).
func assertNoPlatformCalls(t *testing.T, rec *recordingRunner) {
	t.Helper()
	if len(rec.calls) == 0 {
		return
	}
	verbs := make([]string, 0, len(rec.calls))
	for _, call := range rec.calls {
		fields := strings.Fields(call)
		if len(fields) > 1 {
			verbs = append(verbs, fields[1])
		}
	}
	t.Fatalf("the platform runner was called %d time(s): %v", len(rec.calls), verbs)
}

func TestHarvestVault_RenamesBeforeImport(t *testing.T) {
	sealEnv(t)
	scratch := t.TempDir()
	cfg := harvestCustodyConfig(scratch)
	input := inputFor(t, "vault/vault.env")
	fx, err := harvestVault(context.Background(), scratch, input, cfg)
	if err != nil {
		t.Fatal(err)
	}
	names := custodyNames(t, cfg)
	original, err := secrets.ParseVaultEnv(input.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(original) {
		t.Fatalf("custody holds %d names, input has %d entries", len(names), len(original))
	}
	for _, name := range names {
		if !syntheticName.MatchString(name) {
			t.Error("a non-synthetic name reached the custody")
		}
	}
	for _, entry := range original {
		if strings.Contains(string(fx.Data), entry.Name) {
			t.Error("a committed vault name reached the fixture")
		}
	}
}

func TestRenameVaultEntries_Shape(t *testing.T) {
	input := inputFile{Rel: "vault/x.env", Data: []byte("B_KEY=one\nA_KEY='two'\nB_KEY=\"three\"\n")}
	got, err := renameVaultEntries(input)
	if err != nil {
		t.Fatal(err)
	}
	want := "MIG_FIXTURE_SECRET_2=NONSECRET-vault-x-env-2\nMIG_FIXTURE_SECRET_1=NONSECRET-vault-x-env-1\n" +
		"MIG_FIXTURE_SECRET_2=NONSECRET-vault-x-env-2\n"
	if string(got) != want {
		t.Errorf("renamed vault = %q", got)
	}
	if _, err := renameVaultEntries(inputFile{Rel: "vault/bad.env", Data: []byte("no equals sign\n")}); err == nil {
		t.Error("an unparseable vault was accepted")
	}
}

func TestRun_UsesCustodyConfigFor(t *testing.T) {
	newModule(t)
	rec := newRecordingRunner(t)
	dir := filepath.Join(t.TempDir(), "D")
	prev := custodyConfigFor
	custodyConfigFor = func(scratch string) secrets.Config {
		cfg := harvestCustodyConfig(scratch)
		cfg.Dir = dir
		cfg.Runner = rec.run
		return cfg
	}
	t.Cleanup(func() { custodyConfigFor = prev })
	if code, _, stderr := runHarvest(t); code != exitOK {
		t.Fatalf("harvest failed: %s", stderr)
	}
	assertNoPlatformCalls(t, rec)
	names := custodyNames(t, secrets.Config{Service: harvestService, Dir: dir, ForceFileVault: true})
	if len(names) != 3 {
		t.Fatalf("the custody in D holds %d names, want 3", len(names))
	}
	for _, name := range names {
		if !syntheticName.MatchString(name) {
			t.Error("a non-synthetic name reached the custody")
		}
	}
	_ = filepath.WalkDir(scratchParent, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), "vault.") {
			t.Errorf("a vault file exists outside D: %s", d.Name())
		}
		return nil
	})
}

// custodyNames lists the names a file-vault custody holds.
func custodyNames(t *testing.T, cfg secrets.Config) []string {
	t.Helper()
	if _, err := os.Stat(cfg.Dir); err != nil {
		t.Fatalf("no custody dir: %v", err)
	}
	custody, err := secrets.SelectCustody(cfg)
	if err != nil {
		t.Fatal(err)
	}
	names, err := custody.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return names
}
