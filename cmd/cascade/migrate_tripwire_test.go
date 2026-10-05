package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
)

// TestRunMigrateV1_PassesTripwireSeams exercises the production options hunk:
// missing checksums in the working checkout must warn before the first factory.
func TestRunMigrateV1_PassesTripwireSeams(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/acamarata/cascade\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	calls := 0
	deps := migrateDeps{OpenLedger: noopOpenLedger(t), Getenv: func(string) string { return "" }}
	deps.Factory = func(context.Context, migrationv1.Domain) (migrationv1.Importer, func() error, error) {
		calls++
		if !strings.Contains(stderr.String(), filepath.Join(root, "internal/migration/testdata/golden-checksums.sha256")) {
			t.Fatalf("working directory or writer not passed: %q", stderr.String())
		}
		return &recordingImporter{}, func() error { return nil }, nil
	}
	cmd := newMigrateV1Cmd(deps)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--from", root, "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || strings.Count(stderr.String(), "\n") != 1 {
		t.Fatalf("calls=%d warning=%q", calls, stderr.String())
	}
}
