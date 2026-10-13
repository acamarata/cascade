//go:build windows

package claude

// Purpose: the Art.5 tier-2 proof: on windows,
//   Spawn refuses with the ErrPlatform sentinel before any other guard,
//   with or without a daemon socket.
// Constraints: self-contained (the shared fixture is !windows because it
//   runs POSIX shell scripts); the windows CI lane runs this file.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

type windowsProfile struct{ home string }

func (p windowsProfile) Resolve(context.Context) (Profile, error) {
	return Profile{VendorAuthVar: VendorConfigDirVar, RuntimeHome: p.home,
		Posture: provider.NewCompliancePosture(nil, true, true, nil, "steady", false)}, nil
}

type windowsLeaser struct{ root string }

func (l windowsLeaser) Lease(context.Context, provider.WorktreeScope) (Lease, error) {
	return Lease{Root: l.root, GitDir: filepath.Join(l.root, ".git")}, nil
}

func TestClaudeDriverErrPlatformOnWindows(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "harness.exe")
	if err := os.WriteFile(bin, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := New(Config{
		Binary: bin, Profile: windowsProfile{home: root},
		Worktree: windowsLeaser{root: root}, Defaults: provider.NewDriverDefaults(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	spec := provider.AgentJobSpec{Prompt: "x", DataClass: provider.DataClassInternal, WorktreeRoot: root,
		Filter: func(b []byte) ([]byte, error) { return b, nil },
		Env:    provider.SpawnEnv{Driver: []string{"HOME=" + root}, Home: root}}
	for _, socket := range []string{"", `\\.\pipe\cascade-test`} {
		t.Setenv("CASCADE_SOCKET", socket)
		if _, err := d.Spawn(context.Background(), spec); err != provider.ErrPlatform {
			t.Fatalf("Spawn on windows with CASCADE_SOCKET %q = %v, want ErrPlatform", socket, err)
		}
	}
}
