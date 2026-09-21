// Purpose: BuildRunnerConfig tests: repo-type auto-detection (go.mod
// present/absent), explicit [ci.local] overrides, partial overrides, and
// the fail-closed unknown-repo-type error path.
// SPORT: internal.ci.BuildRunnerConfig/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeStat returns a StatFunc that reports name "found" when it exactly
// matches want, and os.ErrNotExist otherwise -- no real filesystem touch.
func fakeStat(want string) StatFunc {
	return func(name string) (os.FileInfo, error) {
		if name == want {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}
}

func alwaysNotFoundStat(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

func TestBuildRunnerConfig_GoDefaults(t *testing.T) {
	cfg, err := BuildRunnerConfig(LocalConfig{}, "/repo", fakeStat("/repo/go.mod"), nil)
	if err != nil {
		t.Fatalf("BuildRunnerConfig: %v", err)
	}
	if len(cfg.Steps) != 3 {
		t.Fatalf("Steps = %v, want 3 (lint, test, build)", cfg.Steps)
	}
	wantOrder := []StepKind{StepLint, StepTest, StepBuild}
	for i, want := range wantOrder {
		if cfg.Steps[i].Kind != want {
			t.Errorf("Steps[%d].Kind = %q, want %q", i, cfg.Steps[i].Kind, want)
		}
	}
	if cfg.TimeoutPerStep != defaultStepTimeout {
		t.Errorf("TimeoutPerStep = %v, want default %v", cfg.TimeoutPerStep, defaultStepTimeout)
	}
}

func TestBuildRunnerConfig_ExplicitOverridesDefaults(t *testing.T) {
	local := LocalConfig{Lint: []string{"custom-lint"}, Test: []string{"custom-test"}, Build: []string{"custom-build"}, TimeoutSeconds: 45}
	cfg, err := BuildRunnerConfig(local, "/repo", fakeStat("/repo/go.mod"), nil)
	if err != nil {
		t.Fatalf("BuildRunnerConfig: %v", err)
	}
	if cfg.Steps[0].Command != "custom-lint" {
		t.Errorf("Steps[0].Command = %q, want custom-lint", cfg.Steps[0].Command)
	}
	wantTimeout := 45
	if int(cfg.TimeoutPerStep.Seconds()) != wantTimeout {
		t.Errorf("TimeoutPerStep = %v, want %ds", cfg.TimeoutPerStep, wantTimeout)
	}
}

func TestBuildRunnerConfig_PartialOverrideFillsRestFromDetection(t *testing.T) {
	local := LocalConfig{Lint: []string{"custom-lint"}}
	cfg, err := BuildRunnerConfig(local, "/repo", fakeStat("/repo/go.mod"), nil)
	if err != nil {
		t.Fatalf("BuildRunnerConfig: %v", err)
	}
	if cfg.Steps[0].Command != "custom-lint" {
		t.Errorf("Steps[0].Command = %q, want custom-lint", cfg.Steps[0].Command)
	}
	if cfg.Steps[1].Command != goDefaultCommands[StepTest][0] {
		t.Errorf("Steps[1].Command = %q, want the Go default", cfg.Steps[1].Command)
	}
}

// TestBuildRunnerConfig_UnknownRepoTypeFailsClosed proves 06 §5.20:
// no go.mod and no [ci.local] commands at all is a hard error, never an
// empty step list silently accepted as "nothing to run".
func TestBuildRunnerConfig_UnknownRepoTypeFailsClosed(t *testing.T) {
	_, err := BuildRunnerConfig(LocalConfig{}, "/repo", alwaysNotFoundStat, nil)
	if err == nil {
		t.Fatal("expected an error for an unrecognized repo type with no [ci.local] commands")
	}
}

// TestBuildRunnerConfig_UnknownRepoTypeButFullyConfigured proves the
// converse: an unrecognized repo type is NOT an error when config.toml
// already supplies all three phases explicitly -- detection is only a
// fallback, never a gate on top of an already-complete configuration.
func TestBuildRunnerConfig_UnknownRepoTypeButFullyConfigured(t *testing.T) {
	local := LocalConfig{Lint: []string{"l"}, Test: []string{"t"}, Build: []string{"b"}}
	cfg, err := BuildRunnerConfig(local, "/repo", alwaysNotFoundStat, nil)
	if err != nil {
		t.Fatalf("BuildRunnerConfig: %v", err)
	}
	if len(cfg.Steps) != 3 {
		t.Fatalf("Steps = %v, want 3", cfg.Steps)
	}
}

func TestBuildRunnerConfig_EmptyRepoRootDefaultsToDot(t *testing.T) {
	cfg, err := BuildRunnerConfig(LocalConfig{Lint: []string{"l"}, Test: []string{"t"}, Build: []string{"b"}}, "", alwaysNotFoundStat, nil)
	if err != nil {
		t.Fatalf("BuildRunnerConfig: %v", err)
	}
	if cfg.RepoRoot != "." {
		t.Errorf("RepoRoot = %q, want \".\"", cfg.RepoRoot)
	}
}

func TestBuildRunnerConfig_NilStatFnUsesOSStat(t *testing.T) {
	// A nil statFn falls back to os.Stat; t.TempDir() genuinely has no
	// go.mod, so a fully-configured LocalConfig must still succeed via
	// the real filesystem path.
	local := LocalConfig{Lint: []string{"l"}, Test: []string{"t"}, Build: []string{"b"}}
	if _, err := BuildRunnerConfig(local, t.TempDir(), nil, nil); err != nil {
		t.Fatalf("BuildRunnerConfig with nil statFn: %v", err)
	}
}

// TestBuildRunnerConfig_EnvIsAllowlisted proves the resolved RunnerConfig
// carries a BUILT environment, not the ambient one: a credential in the
// input does not reach Env, and an operator-named key does.
func TestBuildRunnerConfig_EnvIsAllowlisted(t *testing.T) {
	local := LocalConfig{
		Lint: []string{"l"}, Test: []string{"t"}, Build: []string{"b"},
		EnvKeys: []string{"MY_BUILD_FLAG"},
	}
	environ := []string{"PATH=/bin", "GITHUB_TOKEN=ghp-secret", "MY_BUILD_FLAG=on"}
	cfg, err := BuildRunnerConfig(local, "/repo", alwaysNotFoundStat, environ)
	if err != nil {
		t.Fatalf("BuildRunnerConfig: %v", err)
	}
	joined := strings.Join(cfg.Env, " ")
	if strings.Contains(joined, "GITHUB_TOKEN") {
		t.Errorf("RunnerConfig.Env = %q, want no GITHUB_TOKEN", joined)
	}
	if !strings.Contains(joined, "MY_BUILD_FLAG=on") {
		t.Errorf("RunnerConfig.Env = %q, want the operator-named MY_BUILD_FLAG", joined)
	}
	if !strings.Contains(joined, "CI=true") {
		t.Errorf("RunnerConfig.Env = %q, want CI=true", joined)
	}
}

// TestValidateRepoRoot covers the four --repo outcomes: a real checkout, a
// directory with no .git, a path that is a file, and a path that does not
// exist. Without this check a typo silently runs the gate somewhere else
// and records it under a repo_id derived from the wrong path.
func TestValidateRepoRoot(t *testing.T) {
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"a real checkout", checkout, false},
		{"a directory with no .git", bare, true},
		{"a file, not a directory", file, true},
		{"a path that does not exist", filepath.Join(bare, "nope"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateRepoRoot(c.path, nil); (err != nil) != c.wantErr {
				t.Errorf("ValidateRepoRoot(%q) error = %v, wantErr = %v", c.path, err, c.wantErr)
			}
		})
	}
}

// TestValidateRepoRoot_AcceptsAWorktreeGitFile proves .git may be a FILE:
// that is how git records a linked worktree or a submodule, and refusing it
// would make the gate unusable in exactly the isolated checkouts the fleet
// runs in.
func TestValidateRepoRoot_AcceptsAWorktreeGitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepoRoot(dir, nil); err != nil {
		t.Errorf("ValidateRepoRoot(worktree) = %v, want nil", err)
	}
}
