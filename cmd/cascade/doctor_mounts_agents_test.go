package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
)

const agentLanesTestConfig = `[agents.lanes.codex]
enabled = true
model = "lane-model"
effort = "high"
posture = "programmatic"

[agents.lanes.claude]
posture = "interactive"

[agents.auth.codex]
class = "api_key"
key_env = "LANE_TEST_KEY_SOURCE"
`

// laneAuth is the codex api_key auth table the per-lane tests share.
func laneAuth() map[agents.HarnessID]agents.DriverAuth {
	return map[agents.HarnessID]agents.DriverAuth{
		"codex": {Harness: "codex", Class: "api_key", KeyEnv: "LANE_TEST_KEY_SOURCE"},
	}
}

// laneLines splits a check message into its per-lane lines.
func laneLines(msg string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(msg, "; ") {
		out[strings.Fields(line)[0]] = line
	}
	return out
}

// TestAgentLanesResultIsPerLane proves each lane's class and entitlement
// warning come from that lane alone, and that no key source is printed.
func TestAgentLanesResultIsPerLane(t *testing.T) {
	bindings := agents.Bindings()
	lanes := []runtime.AgentLaneConfig{
		{Driver: "claude", Enabled: true, Posture: runtime.AgentPostureInteractive},
		{Driver: "codex", Enabled: true, Posture: runtime.AgentPostureProgrammatic},
	}
	res := agentLanesResult(lanes, laneAuth(), bindings)
	lines := laneLines(res.Message)
	if l := lines["agent:codex"]; !strings.Contains(l, "class=api_key") || strings.Contains(l, "warning") {
		t.Fatalf("codex line: %q", l)
	}
	if l := lines["agent:claude"]; !strings.Contains(l, "class=unset") || !strings.Contains(l, "(warning: ProgrammaticEntitlement false)") {
		t.Fatalf("claude line: %q", l)
	}
	if res.Status != doctor.StatusWarn {
		t.Fatalf("status = %v, want warn", res.Status)
	}
	if strings.Contains(res.Message, "default") || strings.Contains(res.Message, "LANE_TEST_KEY_SOURCE") {
		t.Fatalf("message leaks or hardcodes: %q", res.Message)
	}
	lanes[0].Posture, lanes[1].Posture = runtime.AgentPostureProgrammatic, runtime.AgentPostureInteractive
	lines = laneLines(agentLanesResult(lanes, laneAuth(), bindings).Message)
	if strings.Contains(lines["agent:claude"], "warning") || !strings.Contains(lines["agent:codex"], "ProgrammaticEntitlement false") {
		t.Fatalf("warning did not move to codex: %v", lines)
	}
}

// TestAgentLanesResultFlagsUnregisteredHarness proves a lane with no binding
// is flagged, and a single bound programmatic lane is clean.
func TestAgentLanesResultFlagsUnregisteredHarness(t *testing.T) {
	bindings := agents.Bindings()
	ghost := []runtime.AgentLaneConfig{{Driver: "ghost", Enabled: true, Posture: runtime.AgentPostureProgrammatic}}
	res := agentLanesResult(ghost, nil, bindings)
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "harness=unregistered") ||
		!strings.Contains(res.Message, "(warning: harness not registered)") {
		t.Fatalf("ghost: %+v", res)
	}
	ok := []runtime.AgentLaneConfig{{Driver: "codex", Enabled: true, Posture: runtime.AgentPostureProgrammatic}}
	if res := agentLanesResult(ok, laneAuth(), bindings); res.Status != doctor.StatusOK {
		t.Fatalf("bound programmatic lane: %+v", res)
	}
}

// TestAgentLanesCheckStates covers the empty and invalid configurations.
func TestAgentLanesCheckStates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	paths := doctorTestPaths(t)
	check := newAgentLanesDoctorCheck(paths)
	res, err := check.Run(context.Background())
	if err != nil || res.Status != doctor.StatusOK || res.Message != "no agent lanes configured" {
		t.Fatalf("empty: %+v %v", res, err)
	}
	body := "[agents.lanes.codex]\nposture = \"bogus\"\n"
	if err := os.WriteFile(filepath.Join(paths.Root(), "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = check.Run(context.Background())
	if err != nil || res.Status != doctor.StatusError || !strings.Contains(res.Message, "agents.lanes.codex.posture") {
		t.Fatalf("invalid: %+v %v", res, err)
	}
	if _, err := check.Fix(context.Background()); !errors.Is(err, doctor.ErrCheckNotFixable) {
		t.Fatalf("Fix = %v, want ErrCheckNotFixable", err)
	}
}

// TestDoctorAgentLanePosture runs the mounted doctor report over a config
// with two lanes and checks it names each lane's posture and class but
// never the key_env name.
func TestDoctorAgentLanePosture(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	paths := doctorTestPaths(t)
	if err := os.WriteFile(filepath.Join(paths.Root(), "config.toml"), []byte(agentLanesTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	useTempCustody(t)
	deps := testDoctorDeps(t)
	deps.Registry = func(ctx context.Context) (*doctor.CheckRegistry, error) {
		return productionCheckRegistry(ctx, paths, doctorTestClock())
	}
	out, err := execRootDoctor(t, deps, "doctor")
	if err != nil && !strings.Contains(err.Error(), "outcome") {
		t.Fatal(err)
	}
	for _, want := range []string{"agent_lanes", "agent:codex", "agent:claude", "posture=programmatic",
		"posture=interactive", "class=api_key", "class=unset", "ProgrammaticEntitlement false"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "LANE_TEST_KEY_SOURCE") {
		t.Fatal("doctor printed a key_env name")
	}
}
