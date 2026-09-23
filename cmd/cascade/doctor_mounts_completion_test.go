// Purpose: proves doctor_mounts_completion.go's mount is real -- the
// production CompletionGateDoctorCheck reports FAIL when the CC harness's
// settings file does not yet name the completion-gate RPC method and OK
// once it does, and the check is actually present on the real
// productionCheckRegistry (the mutation this ticket's checks require:
// deleting the Register call turns TestProductionRegistryMountsCompletion
// GateCheck red).
// SPORT: cmd/cascade/doctor (CompletionGateDoctorCheck mount tests), CR-B D4.
package main

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
)

// completionGateFakeHome points HOME (plugins/claude's own hostEnvFixture
// pattern) at a temp dir, so the real HostPaths resolver never touches the
// operator's own settings file, and returns the settings.json path it
// resolves to.
func completionGateFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE on Windows
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return filepath.Join(home, ".claude", "settings.json")
}

// TestProductionCompletionGateCheck_OKWhenNoHarnessInstalled pins the
// TestDoctorIsMountedOnRoot regression this mount must never repeat: a
// bare $HOME with no CC harness installed at all (no settings.json) is
// "nothing to wire into yet" -- OK, never a FAIL that would break every
// fresh install's `cascade doctor` (mirrors internal/context's own
// harnessResult).
func TestProductionCompletionGateCheck_OKWhenNoHarnessInstalled(t *testing.T) {
	completionGateFakeHome(t)
	res, err := productionCompletionGateCheck().Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run with no settings file = %+v, want StatusOK (no harness installed)", res)
	}
}

// TestProductionCompletionGateCheck_FailsWhenAbsentFromSettings covers
// the real FAIL case: the settings file EXISTS (the harness IS installed)
// but does not name the completion-gate hook's RPC method.
func TestProductionCompletionGateCheck_FailsWhenAbsentFromSettings(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the harness integration refuses on Windows tier-2 (plugins/claude.hostPathsFor), so no settings file is ever read there")
	}
	settingsPath := completionGateFakeHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res, err := productionCompletionGateCheck().Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("Run with an installed settings file missing the hook = %+v, want StatusError", res)
	}
}

func TestProductionCompletionGateCheck_OKWhenPresentInSettings(t *testing.T) {
	settingsPath := completionGateFakeHome(t)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"curl --unix-socket s -d {\"method\":\"fleet.sessions.completion_check\"} http://x"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res, err := productionCompletionGateCheck().Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run with settings file naming the method = %+v, want StatusOK", res)
	}
}

// TestProductionRegistryMountsCompletionGateCheck drives the REAL
// productionCheckRegistry and asserts the completion-gate check is
// present on it.
func TestProductionRegistryMountsCompletionGateCheck(t *testing.T) {
	useTempCustody(t)
	completionGateFakeHome(t)
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	for _, check := range reg.List() {
		if check.Name() == "completion-gate-hooks" {
			return
		}
	}
	t.Fatal("completion-gate-hooks is not registered on the real productionCheckRegistry")
}
