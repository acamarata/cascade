// Purpose: registers the [mcp] guarded config family with internal/runtime.
// The family models mcp.profile, which controls the MCP tool set offered
// to clients. A raise (compact -> compact-write -> full) is a loosening
// that requires config.accept_loosening or an allow verdict.
// Inputs: the config tree's Extra map. Outputs: RegisterGuardedFamily call
// at init and admittedMCPProfile for the mcp command.
// Constraints: fail closed. An invalid profile string is an Extract error.
// SPORT: cmd/cascade guarded mcp (P1-HRN-05e).
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func init() {
	runtime.RegisterGuardedFamily(runtime.GuardedFamily{
		Section: "mcp",
		Keys:    []string{"mcp.profile"},
		Extract: extractMCPFamily,
		Compare: compareMCPFamily,
	})
}

// mcpFamilyValue is the canonical form stored for [mcp].profile.
type mcpFamilyValue struct {
	Profile string `json:"profile"`
}

// extractMCPFamily runs the production decoder over [mcp] and returns the
// canonical JSON value. An absent section is nil (the shipped default);
// an invalid section is an error.
func extractMCPFamily(extra map[string]interface{}) (json.RawMessage, error) {
	raw, ok := extra["mcp"]
	if !ok || raw == nil {
		return nil, nil
	}
	section, ok := raw.(map[string]interface{})
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "mcp: must be a table")
	}
	profileRaw, ok := section["profile"]
	if !ok {
		return nil, nil
	}
	profileStr, ok := profileRaw.(string)
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "mcp.profile: must be a string")
	}
	profile := mcp.ParseToolProfile(profileStr)
	if string(profile) != profileStr {
		return nil, cascade.New(cascade.KindInvalidInput, fmt.Sprintf("mcp.profile: unknown profile %q", profileStr))
	}
	val := mcpFamilyValue{Profile: string(profile)}
	return json.Marshal(val)
}

// mcpProfileRank returns the rank of a profile for comparison purposes.
// compact < compact-write < full.
func mcpProfileRank(p mcp.ToolProfile) int {
	switch p {
	case mcp.ProfileCompact:
		return 0
	case mcp.ProfileCompactWrite:
		return 1
	case mcp.ProfileFull:
		return 2
	}
	return -1
}

// compareMCPFamily reports every key next loosens relative to base. A raise
// (higher rank) is a loosening on "mcp.profile".
func compareMCPFamily(base, next json.RawMessage) ([]runtime.LooseningPath, error) {
	var baseVal, nextVal mcpFamilyValue
	if len(base) > 0 {
		if err := json.Unmarshal(base, &baseVal); err != nil {
			return nil, err
		}
	}
	if len(next) > 0 {
		if err := json.Unmarshal(next, &nextVal); err != nil {
			return nil, err
		}
	}
	baseProfile := mcp.ParseToolProfile(baseVal.Profile)
	nextProfile := mcp.ParseToolProfile(nextVal.Profile)
	baseRank := mcpProfileRank(baseProfile)
	nextRank := mcpProfileRank(nextProfile)
	if nextRank > baseRank {
		return []runtime.LooseningPath{
			{
				Family:   "mcp",
				Key:      "mcp.profile",
				Current:  string(baseProfile),
				Proposed: string(nextProfile),
				Reason:   mcpRaiseReason(baseProfile, nextProfile),
			},
		}, nil
	}
	return nil, nil
}

// mcpRaiseReason names what a raise from base to next newly exposes, so the
// operator who accepts the loosening sees the capabilities it opens. The
// compact-write set is listed by its per-tool capability; the full profile
// has no member filter, so it opens every registered tool.
func mcpRaiseReason(base, next mcp.ToolProfile) string {
	reason := "profile rank increased"
	if next == mcp.ProfileFull {
		return reason + "; exposes every registered tool"
	}
	if base != mcp.ProfileCompact {
		return reason
	}
	caps := make([]string, 0, len(mcp.CompactWriteToolNames))
	for _, tool := range mcp.CompactWriteToolNames {
		caps = append(caps, mcp.ToolCapability(tool))
	}
	return reason + "; exposes " + strings.Join(caps, ", ")
}

// admittedMCPProfile returns the admitted [mcp].profile from eff. When the
// family is absent or nil, it returns mcp.ProfileCompact (the shipped default).
func admittedMCPProfile(eff *runtime.EffectiveConfig) mcp.ToolProfile {
	if eff == nil || eff.Families == nil {
		return mcp.ProfileCompact
	}
	raw, ok := eff.Families["mcp"]
	if !ok || len(raw) == 0 {
		return mcp.ProfileCompact
	}
	var val mcpFamilyValue
	if json.Unmarshal(raw, &val) != nil {
		return mcp.ProfileCompact
	}
	return mcp.ParseToolProfile(val.Profile)
}
