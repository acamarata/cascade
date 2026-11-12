//go:build !windows

// Purpose: the git gate's input refusals, hashing failures and non-repository paths.

package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestGitGateInstallRefusals: an empty repository id, a relative or
// multi-line binary path, a missing binary, a digest mismatch and a
// directory that is not a repository each refuse with their own kind.
func TestGitGateInstallRefusals(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cascade")
	gateOK(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0755))
	digest, err := ComputeSHA256(bin)
	gateOK(t, err)
	repo := baselineRepo(t)
	for name, c := range map[string]struct {
		repo, id, bin, digest string
		kind                  cascade.Kind
	}{
		"empty id":      {repo, "", bin, digest, cascade.KindInvalidInput},
		"relative bin":  {repo, "repo-1", "cascade", digest, cascade.KindInvalidInput},
		"newline bin":   {repo, "repo-1", bin + "\nx", digest, cascade.KindInvalidInput},
		"missing bin":   {repo, "repo-1", bin + ".missing", digest, cascade.KindUnavailable},
		"digest change": {repo, "repo-1", bin, strings.Repeat("0", 64), cascade.KindIntegrity},
	} {
		if err := GitGateInstall(c.repo, c.id, c.bin, c.digest); !cascade.HasKind(err, c.kind) {
			t.Errorf("%s: %v, want %v", name, err, c.kind)
		}
	}
	if err := GitGateInstall(t.TempDir(), "repo-1", bin, digest); err == nil {
		t.Error("install into a directory that is not a repository succeeded")
	}
}

// TestComputeSHA256Refusals: a missing file and a directory are unavailable.
func TestComputeSHA256Refusals(t *testing.T) {
	if _, err := ComputeSHA256(filepath.Join(t.TempDir(), "missing")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("missing = %v", err)
	}
	if _, err := ComputeSHA256(t.TempDir()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("directory = %v", err)
	}
}

// TestGitGateStatusOutsideRepository: status of a plain directory is an
// error, and the result still renders through String.
func TestGitGateStatusOutsideRepository(t *testing.T) {
	s, err := GitGateStatus(t.TempDir(), MirrorState{Enabled: true})
	if err == nil {
		t.Fatal("status outside a repository succeeded")
	}
	if !s.MirrorEnabled || !strings.Contains(s.String(), "hook_installed=false") {
		t.Fatalf("status = %+v / %q", s, s.String())
	}
}

// TestWriteGateHookRefusesBlockedDirectory: a hooks path under a regular
// file cannot be created.
func TestWriteGateHookRefusesBlockedDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	gateOK(t, os.WriteFile(file, nil, 0o600))
	if err := writeGateHook(filepath.Join(file, "hooks", "pre-receive"), []byte("x")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("blocked = %v, want KindUnavailable", err)
	}
}
