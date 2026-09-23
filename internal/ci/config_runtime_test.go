// Purpose (this file): TestConfigFromRuntime* proves ConfigFromRuntime
// (config_runtime.go, final-confirm round 3 fix) maps a real, loaded
// internal/runtime.Config's [ci].affected_cmd into ci.Config verbatim,
// end-to-end through a real affected_cmd subprocess dispatch -- closing
// the AC's "internal/ci/affected_cmd.go reads it from there, not from an
// unsourced field" gap that TestConfigCIAffectedCmd (internal/runtime)
// and TestAffectedTargets_AffectedCmdPresent (internal/ci) each proved
// only half of, in isolation.

package ci

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// loadRuntimeConfigForTest writes toml to a real config.toml under
// t.TempDir() and loads it via a real internal/runtime.Load call -- no
// test double for the config-loading path.
func loadRuntimeConfigForTest(t *testing.T, toml string) *runtime.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
	cfg, err := runtime.Load(context.Background(), runtime.LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	})
	if err != nil {
		t.Fatalf("runtime.Load: %v", err)
	}
	return cfg
}

// TestConfigFromRuntime_MapsAffectedCmdVerbatim proves the field mapping
// alone: a loaded runtime.Config's AffectedCmd lands on ci.Config
// unchanged, and an absent key still maps to "" (not configured).
func TestConfigFromRuntime_MapsAffectedCmdVerbatim(t *testing.T) {
	rtCfg := loadRuntimeConfigForTest(t, "[ci]\naffected_cmd = \"printf 'pkg/from-runtime\\n'\"\n")
	cfg := ConfigFromRuntime(*rtCfg)
	// The TOML file's own basic-string "\n" escape decodes to a real
	// newline by the time the TOML parser hands the value back -- this
	// is TOML's own escaping, not something ConfigFromRuntime or
	// parseCIAffectedCmdField re-interprets; the "want" here matches
	// what the real toml decoder actually produces.
	want := "printf 'pkg/from-runtime\n'"
	if cfg.AffectedCmd != want {
		t.Fatalf("ConfigFromRuntime.AffectedCmd = %q, want %q", cfg.AffectedCmd, want)
	}

	rtCfgAbsent := loadRuntimeConfigForTest(t, "")
	cfgAbsent := ConfigFromRuntime(*rtCfgAbsent)
	if cfgAbsent.AffectedCmd != "" {
		t.Fatalf("ConfigFromRuntime.AffectedCmd = %q, want empty when unconfigured", cfgAbsent.AffectedCmd)
	}
}

// TestConfigFromRuntime_AffectedCmdEndToEnd proves the end-to-end path
// the final-confirm round 3 fix list asked for (item 3): a real
// config.toml's [ci].affected_cmd, loaded via internal/runtime, threaded
// through ConfigFromRuntime into a RequirementModel whose Affected call
// actually exercises affected_cmd.go's real subprocess dispatch --
// proving ci.Config.AffectedCmd is no longer an unsourced field.
func TestConfigFromRuntime_AffectedCmdEndToEnd(t *testing.T) {
	rtCfg := loadRuntimeConfigForTest(t, "[ci]\naffected_cmd = \"printf 'pkg/real-target\\n'\"\n")
	model := RequirementModel{WorktreeRoot: t.TempDir(), Stack: "generic", Cfg: ConfigFromRuntime(*rtCfg)}
	targets, err := model.Affected(context.Background(), []string{"whatever.go"})
	if err != nil {
		t.Fatalf("Affected: %v", err)
	}
	want := []Target{"pkg/real-target"}
	if len(targets) != 1 || targets[0] != want[0] {
		t.Fatalf("Affected targets = %v, want %v", targets, want)
	}
}
