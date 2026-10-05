// Purpose: the clean room turns Go telemetry off before the first go command
// so the run directory is removed by a single RemoveAll (no late counter
// writes, no retry, no wait).
//
// SPORT: internal.ci.cleanRoomEnv/TESTED (P1-CI-01).
package ci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTelemetryModePath(t *testing.T) {
	home := filepath.Join("h", "run")
	for goos, want := range map[string]string{
		"darwin":  filepath.Join(home, "Library", "Application Support", "go", "telemetry", "mode"),
		"linux":   filepath.Join(home, ".config", "go", "telemetry", "mode"),
		"windows": filepath.Join(home, "AppData", "Roaming", "go", "telemetry", "mode"),
	} {
		if got := telemetryModePath(goos, home); got != want {
			t.Errorf("telemetryModePath(%s) = %q, want %q", goos, got, want)
		}
	}
}

// TestCleanRoomEnvTurnsGoTelemetryOff checks the mode file says off before
// any go command ran, and that the installed toolchain agrees, resolving its
// telemetry directory under the run HOME.
func TestCleanRoomEnvTurnsGoTelemetryOff(t *testing.T) {
	runDir := t.TempDir()
	env, err := cleanRoomEnv([]string{"PATH=" + os.Getenv("PATH")}, nil, runDir, filepath.Join(runDir, "mod"))
	if err != nil {
		t.Fatalf("cleanRoomEnv: %v", err)
	}
	home := filepath.Join(runDir, "home")
	raw, err := os.ReadFile(telemetryModePath(runtime.GOOS, home))
	if err != nil || strings.TrimSpace(string(raw)) != "off" {
		t.Fatalf("mode file before the first go command = %q, %v; want off", raw, err)
	}
	cmd := exec.Command("go", "env", "GOTELEMETRY", "GOTELEMETRYDIR")
	cmd.Dir, cmd.Env = runDir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go env: %v\n%s", err, out)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) < 2 || lines[0] != "off" {
		t.Fatalf("go env GOTELEMETRY = %q, want off", out)
	}
	if want := filepath.Dir(telemetryModePath(runtime.GOOS, home)); !strings.EqualFold(strings.Join(lines[1:], " "), want) {
		t.Fatalf("go telemetry dir = %q, want %q", strings.Join(lines[1:], " "), want)
	}
}

// TestLocalExecutorRemovesRunDirInOneRemoveAll runs a real go build in the
// clean room and requires the run directory to be gone afterwards: Run
// removes it with exactly one RemoveAll and no retry.
func TestLocalExecutorRemovesRunDirInOneRemoveAll(t *testing.T) {
	skipWithoutPOSIXShell(t)
	r := newRig(t)
	root := t.TempDir()
	var rec runDirEnv
	ex, err := NewLocalSubJobExecutor(LocalExecutorDeps{
		CIDB: r.ciDB, Clock: newTestClock(), Exec: ShellExecutor{},
		Commands: map[RequirementKind][]string{RequirementCompile: {"go build ./... && go vet ./..."}},
		Environ:  []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()},
		RunRoot:  root, ModCache: filepath.Join(t.TempDir(), "mod"),
		Populate:      func(context.Context, string, []string) error { return nil },
		OnEnvironment: func(e Environment) { rec.env = e },
	})
	if err != nil {
		t.Fatalf("NewLocalSubJobExecutor: %v", err)
	}
	r.commit(map[string]string{"go.mod": "module example.test/app\n\ngo 1.21\n", "app.go": "package app\n"})
	sj := SubJob{Ref: r.ref, Kind: RequirementCompile, Snapshot: CandidateSnapshot{TreeHash: treeOf(t, r.repo, r.ref.CheckpointCommit), AttemptID: "rm"},
		Plan: CIRequirementPlan{Selection: TargetSelectionFull}}
	res, err := ex.Run(context.Background(), sj)
	if err != nil || !res.Passed {
		t.Fatalf("Run = %+v, %v; want a passed run", res, err)
	}
	if _, err := os.Stat(rec.env.RunDir); !os.IsNotExist(err) {
		t.Fatalf("run dir %s still exists after Run (stat err %v)", rec.env.RunDir, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("run root not empty after Run: %v", entries)
	}
}
