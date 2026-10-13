//go:build integration && !windows

package claude

// Purpose: TestClaudeDriverIntegration, the Art.2 real-counterpart run
//   against the installed harness: one Spawn/Collect with ToolPolicy none.
// Inputs: CASCADE_TEST_CLAUDE_BINARY (absolute path to the harness) and
//   CASCADE_TEST_CLAUDE_HOME (a 0700 runtime home whose "config" child is
//   an authenticated vendor config directory).
// Constraints: build tag integration. With the binary variable unset the
//   test skips with an asserted refusal, so no default check makes a paid
//   model call. The per-spawn filter is the identity; the real substitution
//   pass arrives with the AD/S-62.T3 host shim.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestClaudeDriverIntegration(t *testing.T) {
	bin := os.Getenv("CASCADE_TEST_CLAUDE_BINARY")
	if bin == "" {
		t.Skip("CASCADE_TEST_CLAUDE_BINARY not set")
	}
	home := os.Getenv("CASCADE_TEST_CLAUDE_HOME")
	if home == "" {
		t.Fatal("CASCADE_TEST_CLAUDE_HOME must name a 0700 runtime home holding an authenticated config/ dir")
	}
	token := os.Getenv(KeyVarOAuthToken)
	if token == "" {
		t.Fatalf("%s must carry the daemon-env OAuth token for the live leg", KeyVarOAuthToken)
	}
	root := newGitWorktree(t)
	prof := Profile{VendorAuthVar: VendorConfigDirVar, RuntimeHome: home, CredentialClass: CredentialClassOAuthToken, KeyVar: KeyVarOAuthToken,
		Posture: provider.NewCompliancePosture([]string{"oauth"}, true, true, []string{"agent"}, "steady", false)}
	d, err := New(Config{Binary: bin, Profile: fakeProfile{prof: prof},
		Worktree: fakeLeaser{lease: Lease{Root: root, GitDir: filepath.Join(root, ".git")}},
		Defaults: provider.NewDriverDefaults()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	spec := provider.AgentJobSpec{Prompt: "Reply with the single word: ok", DataClass: provider.DataClassPublic,
		WorktreeRoot: root, Filter: identity,
		Env: provider.SpawnEnv{Home: home, Driver: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home,
			VendorConfigDirVar + "=" + filepath.Join(home, "config"), KeyVarOAuthToken + "=" + token}}}
	res, err := d.Spawn(ctx, spec)
	if err != nil {
		t.Fatalf("Spawn against the real harness: %v", err)
	}
	cr, err := d.Collect(ctx, res.JobID)
	if err != nil {
		t.Fatalf("Collect against the real harness: %v", err)
	}
	if cr.SessionID == "" || cr.Cost == nil || cr.Cost.Source != provider.CostSourceCLIObservation {
		t.Fatalf("result %+v lacks the revision-1 fields", cr)
	}
}
