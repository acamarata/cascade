package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// agentsTree builds an Extra tree with the given [agents] table.
func agentsTree(agents map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"agents": agents}
}

func wantInvalid(t *testing.T, err error, key string) {
	t.Helper()
	if k, ok := cascade.KindOf(err); err == nil || !ok || k != cascade.KindInvalidInput {
		t.Fatalf("want KindInvalidInput naming %q, got %v", key, err)
	}
	if !strings.Contains(err.Error(), key) {
		t.Fatalf("error %q does not name %q", err, key)
	}
}

func TestParseAgentLanesDefaultsAndOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	lanes, err := ParseAgentLanes(agentsTree(map[string]interface{}{"lanes": map[string]interface{}{
		"codex":  map[string]interface{}{"posture": "programmatic", "model": "m1", "effort": "high"},
		"claude": map[string]interface{}{},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	want := []AgentLaneConfig{
		{Driver: "claude", Enabled: true, Posture: AgentPostureInteractive},
		{Driver: "codex", Enabled: true, Model: "m1", Effort: "high", Posture: AgentPostureProgrammatic},
	}
	if len(lanes) != 2 || lanes[0] != want[0] || lanes[1] != want[1] {
		t.Fatalf("lanes = %+v, want %+v", lanes, want)
	}
	if !lanes[1].ProgrammaticEntitlement() || lanes[0].ProgrammaticEntitlement() {
		t.Fatal("entitlement wrong")
	}
	for _, in := range []map[string]interface{}{{}, agentsTree(map[string]interface{}{})} {
		if l, err := ParseAgentLanes(in); l != nil || err != nil {
			t.Fatalf("absent = %v, %v", l, err)
		}
	}
}

func TestParseAgentLanesRefusals(t *testing.T) {
	lane := func(k string, v interface{}) map[string]interface{} {
		return agentsTree(map[string]interface{}{"lanes": map[string]interface{}{"codex": map[string]interface{}{k: v}}})
	}
	cases := []struct {
		name string
		in   map[string]interface{}
		key  string
	}{
		{"posture", lane("posture", "bogus"), "agents.lanes.codex.posture"},
		{"enabled", lane("enabled", "yes"), "agents.lanes.codex.enabled"},
		{"key_env", lane("key_env", "X"), "agents.lanes.codex.key_env"},
		{"name", agentsTree(map[string]interface{}{"lanes": map[string]interface{}{"Bad:Name": map[string]interface{}{}}}), "agents.lanes.Bad:Name"},
		{"table", agentsTree(map[string]interface{}{"lanes": "x"}), "agents.lanes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseAgentLanes(c.in)
			wantInvalid(t, err, c.key)
		})
	}
}

func TestParseAgentsConfigDelegation(t *testing.T) {
	deleg := func(kv ...interface{}) map[string]interface{} {
		m := map[string]interface{}{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return agentsTree(map[string]interface{}{"delegation": m})
	}
	got, err := ParseAgentsConfig(map[string]interface{}{})
	if err != nil || got.Delegation.WallClock != 30*time.Minute || got.Delegation.MaxWallClock != 24*time.Hour {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	got, err = ParseAgentsConfig(deleg("wall_clock", "45m"))
	if err != nil || got.Delegation.WallClock != 45*time.Minute {
		t.Fatalf("45m = %+v, %v", got, err)
	}
	bad := []struct {
		in  map[string]interface{}
		key string
	}{
		{deleg("max_wall_clock", "25h"), "max_wall_clock"},
		{deleg("wall_clock", "2h", "max_wall_clock", "1h"), "wall_clock"},
		{deleg("wall_clock", "0s"), "wall_clock"},
		{deleg("retries", "3"), "retries"},
	}
	for _, b := range bad {
		_, err := ParseAgentsConfig(b.in)
		wantInvalid(t, err, b.key)
	}
}

func TestAgentsConfigLaneModel(t *testing.T) {
	cfg, err := ParseAgentsConfig(agentsTree(map[string]interface{}{"lanes": map[string]interface{}{
		"codex":  map[string]interface{}{"model": "m1", "effort": "high"},
		"claude": map[string]interface{}{},
		"off":    map[string]interface{}{"enabled": false, "model": "m2"},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	for lane, want := range map[string][2]string{
		"agent:codex": {"m1", "high"}, "agent:claude": {"", ""}, "agent:none": {"", ""}, "agent:off": {"", ""}, "codex": {"", ""},
	} {
		if m, e := cfg.LaneModel(lane); m != want[0] || e != want[1] {
			t.Errorf("%s = %q,%q want %v", lane, m, e, want)
		}
	}
}
