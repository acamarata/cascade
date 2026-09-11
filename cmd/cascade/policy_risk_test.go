package main

// Purpose: covers `policy risk explain` — flag parsing, the R-21.182
//   footprint union and de-duplication (including a pure-rename case
//   classified on its pre-image), the `floor: critical` provenance
//   rendering, --json rendering, and the error paths.
// Constraints: Art.7.1 — every case roots config.toml at t.TempDir().

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestUnionFootprintPaths_DeduplicatesAcrossGroups(t *testing.T) {
	got := unionFootprintPaths(
		[]string{"a.go", "b.go"},
		[]string{"b.go", "c.go"},
		[]string{"a.go"},
		[]string{"d.go", ""},
	)
	want := []string{"a.go", "b.go", "c.go", "d.go"}
	if len(got) != len(want) {
		t.Fatalf("unionFootprintPaths = %v, want %v", got, want)
	}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("unionFootprintPaths[%d] = %q, want %q", i, got[i], p)
		}
	}
}

// TestPolicyRiskExplain_FootprintUnion proves a pure-rename commit (a
// path present only as --post, never as --pre) still classifies: the
// R-21.182 union means a positional/--pre/--post/--symbol-reach path
// contributes to the SAME footprint the classifier sees.
func TestPolicyRiskExplain_FootprintUnion(t *testing.T) {
	footprint := unionFootprintPaths(nil, []string{"old/plain.go"}, []string{"internal/secrets/new.go"}, nil)
	result, err := explainRiskFootprint(footprint, nil)
	if err != nil {
		t.Fatalf("explainRiskFootprint: %v", err)
	}
	if result.RiskClass != "critical" {
		t.Errorf("RiskClass = %q, want critical (post-image alone under internal/secrets/)", result.RiskClass)
	}
	if !result.Floor {
		t.Error("Floor = false, want true (internal/secrets/ is a Critical-floor category)")
	}
}

// TestPolicyRiskExplain_CriticalFloor asserts the floor's matching path
// is named in the result.
func TestPolicyRiskExplain_CriticalFloor(t *testing.T) {
	result, err := explainRiskFootprint([]string{"internal/policy/x.go", "docs/readme.md"}, nil)
	if err != nil {
		t.Fatalf("explainRiskFootprint: %v", err)
	}
	if !result.Floor || len(result.FloorPaths) != 1 || result.FloorPaths[0] != "internal/policy/x.go" {
		t.Errorf("Floor/FloorPaths = %v/%v, want true/[internal/policy/x.go]", result.Floor, result.FloorPaths)
	}
}

// TestPolicyRiskExplain_NoFloor_NormalPath asserts a plain path outside
// every rule classifies Normal, with no floor.
func TestPolicyRiskExplain_NoFloor_NormalPath(t *testing.T) {
	result, err := explainRiskFootprint([]string{"cmd/cascade/other.go"}, nil)
	if err != nil {
		t.Fatalf("explainRiskFootprint: %v", err)
	}
	if result.RiskClass != "normal" || result.Floor {
		t.Errorf("RiskClass/Floor = %q/%v, want normal/false", result.RiskClass, result.Floor)
	}
}

// TestPolicyRiskExplain_GateProvenance asserts an overlay addition is
// reported as "overlay" and a table default as "table".
func TestPolicyRiskExplain_GateProvenance(t *testing.T) {
	result, err := explainRiskFootprint([]string{"cmd/cascade/other.go"}, nil)
	if err != nil {
		t.Fatalf("explainRiskFootprint: %v", err)
	}
	for _, g := range result.Gates {
		if g.Provenance != "table" {
			t.Errorf("gate %q provenance = %q, want table (empty overlay)", g.Gate, g.Provenance)
		}
	}
}

// TestPolicyRiskExplainCmd_RequiresAPath asserts the command refuses
// with no positional, --pre, --post or --symbol-reach path at all.
func TestPolicyRiskExplainCmd_RequiresAPath(t *testing.T) {
	dir := t.TempDir()
	deps := policyRiskDeps{
		Paths:   fakeDaemonPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	cmd := newPolicyRiskExplainCmd(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("policy risk explain with no paths: expected an error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestPolicyRiskExplainCmd_JSONRendering drives the real cobra RunE end
// to end (--json), against a config.toml carrying a [policy.risk_gates]
// overlay addition, and asserts the overlay gate appears with
// provenance "overlay".
func TestPolicyRiskExplainCmd_JSONRendering(t *testing.T) {
	dir := t.TempDir()
	cfg := "[policy.risk_gates]\nlow = [\"human_approval\"]\n"
	if err := os.WriteFile(dir+"/config.toml", []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	deps := policyRiskDeps{
		Paths:   fakeDaemonPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	cmd := newPolicyRiskExplainCmd(deps)
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"docs/readme.md"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out.String(), "\"risk_class\"") || !strings.Contains(out.String(), "low") {
		t.Errorf("json output missing expected fields: %s", out.String())
	}
}

// TestPolicyRiskExplainCmd_MalformedConfig asserts a malformed
// config.toml refuses rather than silently loading the empty overlay.
func TestPolicyRiskExplainCmd_MalformedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/config.toml", []byte("this is not [ valid toml"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	deps := policyRiskDeps{
		Paths:   fakeDaemonPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	cmd := newPolicyRiskExplainCmd(deps)
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"docs/readme.md"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("policy risk explain with malformed config.toml: expected an error, got nil")
	}
}

// TestPolicyRiskExplainView_String asserts the human rendering carries
// the class, the floor line and every gate row.
func TestPolicyRiskExplainView_String(t *testing.T) {
	v := policyRiskExplainView{
		RiskClass:  "critical",
		Floor:      true,
		FloorPaths: []string{"internal/secrets/x.go"},
		Gates:      []gateProvenance{{Gate: "format", Provenance: "table"}, {Gate: "human_approval", Provenance: "overlay"}},
	}
	got := v.String()
	for _, want := range []string{"critical", "internal/secrets/x.go", "format", "table", "human_approval", "overlay"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}
