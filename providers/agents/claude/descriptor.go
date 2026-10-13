// Purpose: the harness Descriptor (R-14.288/R-14.291): per-flag minimum
//   versions, the one-turn probe argv, and the ToolPolicy flag mappings,
//   plus the numeric semver comparison the version gates use.
// Inputs: a probed harness version string and a flag name.
// Outputs: NewDescriptor's values; gate/supports verdicts.
// Constraints: the vendor spellings are pinned by
//   testdata/tool_policy_mappings.golden.json and
//   testdata/protocol/1/probe_argv.golden.json, captured from the installed
//   harness's --help with provenance; those goldens, not this file, are the
//   truth (TestToolPolicyMappingClaudeGolden, TestProbeArgvGolden). A
//   version below a gate is ErrHarnessIncompatible, never a silent omission
//   (the one declared exception is --plugin-dir, whose fallback is the
//   hook-pack copy, see protocol.go). A flag the probed --help does not list
//   is unsupported (R-14.329): a DriverDefaults value for one refuses Spawn
//   with ErrHarnessIncompatible before any child starts.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

package claude

import (
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Descriptor describes the vendor harness surface this driver speaks.
type Descriptor struct {
	// MinVersions maps a flag to the first harness version that has it.
	MinVersions map[string]string
	// ProbeArgv is the one-turn stream-json ping whose init event is
	// checked before the job's first turn.
	ProbeArgv []string
	// ToolPolicyMappings maps none|readonly|all to their flag lists.
	ToolPolicyMappings map[string][]string
}

// disallowedForNone is the R-14.291 write-and-execute tool list.
var disallowedForNone = []string{
	"Write", "Edit", "MultiEdit", "NotebookEdit", "Bash", "Agent", "Task", "WebFetch", "WebSearch",
}

// unsupportedFlags are the flags the probed 2.1.273 --help does not list,
// pinned by probe_argv.golden.json's unsupported_flags, with the
// DriverDefaults field that would emit each.
var unsupportedFlags = []struct{ flag, field string }{
	{"--append-subagent-system-prompt-file", "SubagentKernelPromptFile"},
	{"--max-turns", "MaxTurns"},
}

// refuseUnsupportedDefaults refuses a non-empty/non-zero DriverDefaults
// value for an unsupported flag, naming the flag and the field.
func refuseUnsupportedDefaults(def provider.DriverDefaults) error {
	set := map[string]bool{
		"SubagentKernelPromptFile": def.SubagentKernelPromptFile != "",
		"MaxTurns":                 def.MaxTurns != 0,
	}
	for _, u := range unsupportedFlags {
		if set[u.field] {
			return cascade.Wrapf(cascade.KindConflict, provider.ErrHarnessIncompatible,
				"claude: DriverDefaults.%s is set but the harness --help does not list %s", u.field, u.flag)
		}
	}
	return nil
}

// NewDescriptor returns the descriptor for the harness line this driver
// was verified against (2.1.273, testdata/README.md).
func NewDescriptor() Descriptor {
	readonly := make([]string, 0, len(disallowedForNone))
	for _, tool := range disallowedForNone {
		if tool != "WebFetch" && tool != "WebSearch" {
			readonly = append(readonly, tool)
		}
	}
	probe := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-prompts", "none", "--strict-mcp-config",
		"--setting-sources", "project", "--disallowedTools",
	}
	return Descriptor{
		MinVersions: map[string]string{
			"--permission-prompts": "2.1.259",
			"--plugin-dir":         "2.1.265",
		},
		ProbeArgv: append(probe, disallowedForNone...),
		ToolPolicyMappings: map[string][]string{
			provider.ToolPolicyNone:     append([]string{"--disallowedTools"}, disallowedForNone...),
			provider.ToolPolicyReadonly: append([]string{"--disallowedTools"}, readonly...),
			provider.ToolPolicyAll:      {"--permission-mode", "bypassPermissions"},
		},
	}
}

// supports reports whether version meets flag's minimum (true when the
// flag has no gate). An unparsable version is an error, never "supported".
func (d Descriptor) supports(flag, version string) (bool, error) {
	floor, gated := d.MinVersions[flag]
	if !gated {
		return true, nil
	}
	cmp, err := compareSemver(version, floor)
	if err != nil {
		return false, err
	}
	return cmp >= 0, nil
}

// gate refuses with ErrHarnessIncompatible when version is below flag's
// minimum or cannot be compared.
func (d Descriptor) gate(flag, version string) error {
	ok, err := d.supports(flag, version)
	if err != nil {
		return cascade.Wrapf(cascade.KindConflict, provider.ErrHarnessIncompatible,
			"claude: cannot compare harness version %q for %s", version, flag)
	}
	if !ok {
		return cascade.Wrapf(cascade.KindConflict, provider.ErrHarnessIncompatible,
			"claude: harness %s is below %s, the minimum for %s", version, d.MinVersions[flag], flag)
	}
	return nil
}

// compareSemver compares dotted numeric triples: -1, 0 or 1. Plain string
// order would rank "2.1.21" above "2.1.205"; this never does.
func compareSemver(a, b string) (int, error) {
	pa, okA := parseSemver(a)
	pb, okB := parseSemver(b)
	if !okA || !okB {
		return 0, cascade.Newf(cascade.KindInvalidInput, "claude: %q or %q is not major.minor.patch", a, b)
	}
	for i := range pa {
		switch {
		case pa[i] < pb[i]:
			return -1, nil
		case pa[i] > pb[i]:
			return 1, nil
		}
	}
	return 0, nil
}

// parseSemver parses "major.minor.patch" with non-negative integers only.
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	fields := strings.Split(v, ".")
	if len(fields) != 3 {
		return out, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || f == "" || strings.HasPrefix(f, "+") {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
