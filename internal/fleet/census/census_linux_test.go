//go:build linux

package census

import (
	"os"
	"path/filepath"
	"testing"
)

// Purpose: linux's enumerateRaw against a synthetic /proc tree under
//
//	t.TempDir, so this test never depends on what happens to be running
//	on the machine (AGENT-BRIEF.md's determinism requirement).
//
// SPORT: fleet/census (ADD, per T-1 sport_updates).

func writeSyntheticProc(t *testing.T, entries map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for pidDir, cmdline := range entries {
		dir := filepath.Join(root, pidDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if cmdline == "" {
			continue // no cmdline file: simulates an unreadable/racy pid
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return root
}

func TestLinuxEnumerateRawHappyPath(t *testing.T) {
	orig := procRoot
	defer func() { procRoot = orig }()

	procRoot = writeSyntheticProc(t, map[string]string{
		"123":       "/usr/local/bin/claude\x00--json\x00",
		"456":       "/usr/local/bin/opencode\x00",
		"self":      "", // non-numeric entry, must be skipped without a cmdline read attempt
		"999":       "", // numeric entry with no cmdline file: EACCES/ENOENT-shaped, skip only this pid
		"not-a-pid": "irrelevant",
	})

	raws, err := enumerateRaw()
	if err != nil {
		t.Fatalf("enumerateRaw: %v", err)
	}
	if len(raws) != 2 {
		t.Fatalf("len(raws) = %d, want 2: %+v", len(raws), raws)
	}
	byPid := map[int][]string{}
	for _, r := range raws {
		byPid[r.pid] = r.argv
	}
	if len(byPid[123]) != 2 || byPid[123][0] != "/usr/local/bin/claude" {
		t.Fatalf("pid 123 argv = %v", byPid[123])
	}
	if len(byPid[456]) != 1 {
		t.Fatalf("pid 456 argv = %v", byPid[456])
	}
}

func TestLinuxEnumerateRawEmptyProc(t *testing.T) {
	orig := procRoot
	defer func() { procRoot = orig }()
	procRoot = t.TempDir()

	raws, err := enumerateRaw()
	if err != nil {
		t.Fatalf("enumerateRaw: %v", err)
	}
	if len(raws) != 0 {
		t.Fatalf("raws = %+v, want empty", raws)
	}
}

func TestLinuxEnumerateRawMissingProcRoot(t *testing.T) {
	orig := procRoot
	defer func() { procRoot = orig }()
	procRoot = filepath.Join(t.TempDir(), "does-not-exist")

	if _, err := enumerateRaw(); err == nil {
		t.Fatal("enumerateRaw: want error for missing procRoot, got nil")
	}
}
