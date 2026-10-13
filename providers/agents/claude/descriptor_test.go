//go:build !windows

package claude

// Purpose: the Descriptor against its goldens (captured from the installed
//   harness's --help, testdata provenance) and the numeric semver compare.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// flagInHelp reports whether flag is an option the captured --help lists,
// or a parser-verified hidden flag the probe golden records that is not in
// its unsupported_flags list.
func flagInHelp(t *testing.T, flag string) bool {
	t.Helper()
	help := string(readTestdata(t, "protocol/1/help-excerpt.txt"))
	if strings.Contains(help, "  "+flag+" ") || strings.Contains(help, ", "+flag+" ") ||
		strings.Contains(help, "  "+flag+"\n") || strings.Contains(help, "  "+flag+",") {
		return true
	}
	g := loadProbeGolden(t)
	_, hidden := g.Provenance.Hidden[flag]
	for _, u := range g.Unsupported {
		if u == flag {
			return false
		}
	}
	return hidden
}

type probeGolden struct {
	Argv       []string `json:"probe_argv"`
	Provenance struct {
		Hidden map[string]string `json:"hidden_flags"`
	} `json:"provenance"`
	Unsupported []string `json:"unsupported_flags"`
}

func loadProbeGolden(t *testing.T) probeGolden {
	t.Helper()
	var g probeGolden
	if err := json.Unmarshal(readTestdata(t, "protocol/1/probe_argv.golden.json"), &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestToolPolicyMappingClaudeGolden(t *testing.T) {
	var g struct {
		Mappings map[string][]string `json:"mappings"`
	}
	if err := json.Unmarshal(readTestdata(t, "tool_policy_mappings.golden.json"), &g); err != nil {
		t.Fatal(err)
	}
	got := NewDescriptor().ToolPolicyMappings
	if !reflect.DeepEqual(got, g.Mappings) {
		t.Fatalf("ToolPolicyMappings\n%v\nwant golden\n%v", got, g.Mappings)
	}
	for policy, flags := range g.Mappings {
		for _, tok := range flags {
			if strings.HasPrefix(tok, "--") && !flagInHelp(t, tok) {
				t.Fatalf("%s: %s is not in the captured --help", policy, tok)
			}
		}
	}
	if !strings.Contains(string(readTestdata(t, "protocol/1/help-excerpt.txt")), `"bypassPermissions"`) {
		t.Fatal("the captured --help does not list bypassPermissions")
	}
}

func TestProbeArgvGolden(t *testing.T) {
	g := loadProbeGolden(t)
	got := NewDescriptor().ProbeArgv
	if !reflect.DeepEqual(got, g.Argv) {
		t.Fatalf("ProbeArgv\n%q\nwant golden\n%q", got, g.Argv)
	}
	for _, tok := range got {
		if strings.HasPrefix(tok, "--") && !flagInHelp(t, tok) {
			t.Fatalf("%s is neither in the captured --help nor a verified hidden flag", tok)
		}
	}
	if !hasPair(got, "--output-format", "stream-json") || has(got, "--max-turns") {
		t.Fatalf("ProbeArgv %q is not a stream-json ping free of unsupported flags", got)
	}
	want := make([]string, 0, len(unsupportedFlags))
	for _, u := range unsupportedFlags {
		want = append(want, u.flag)
	}
	if !reflect.DeepEqual(want, g.Unsupported) {
		t.Fatalf("unsupportedFlags %q, want the golden's %q", want, g.Unsupported)
	}
}

func TestSpawnArgvFlagsAreHelpSpelled(t *testing.T) {
	g := loadArgvGolden(t, "/r", "/h", "p")
	for _, tok := range append(g.W6, g.Populated...) {
		if strings.HasPrefix(tok, "--") && len(tok) > 2 && !flagInHelp(t, tok) {
			t.Fatalf("golden argv flag %s is not a captured spelling", tok)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.1.21", "2.1.205", -1}, {"2.1.273", "2.1.259", 1}, {"2.1.259", "2.1.259", 0}, {"3.0.0", "2.9.9", 1},
	}
	for _, c := range cases {
		got, err := compareSemver(c.a, c.b)
		if err != nil || got != c.want {
			t.Fatalf("compareSemver(%s,%s) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range []string{"2.1", "2.1.x", "2.1.-1", "2..1", "+1.2.3", "2.1.+3"} {
		if _, err := compareSemver(bad, "1.0.0"); err == nil {
			t.Fatalf("compareSemver accepted %q", bad)
		}
	}
	d := NewDescriptor()
	if ok, err := d.supports("--add-dir", "anything"); !ok || err != nil {
		t.Fatal("an ungated flag must be supported")
	}
	if err := d.gate("--plugin-dir", "junk"); err == nil {
		t.Fatal("gate accepted an unparsable version")
	}
}
