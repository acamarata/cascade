// Package v1 uses this file for closed account enum mappings and validation tables.
// Purpose: validate and translate closed v1 account enumerations.
// Inputs: schema-version 1 family, role, access-method and task-class strings.
// Outputs: exact v2 provider vocabulary values or an unknown-enum refusal.
// Constraints: no default arms guess at new v1 values.
// SPORT: migration/v1/accounts/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"strconv"

	"github.com/acamarata/cascade/internal/providers/registry"
)

func mapV1Driver(family string) (registry.DriverKind, bool) {
	switch family {
	case "claude":
		return registry.DriverAnthropic, true
	case "openai":
		return registry.DriverOpenAICompat, true
	case "google", "gfp":
		return registry.DriverGemini, true
	case "opencode", "zai":
		return "", false
	default:
		return "", false
	}
}

func mapV1Role(role string) (registry.AccountKind, registry.Tier, bool) {
	switch role {
	case "primary-t0":
		return registry.AccountPersonal, registry.TierStrongest, true
	case "pooled":
		return registry.AccountShared, registry.TierStrong, true
	case "free":
		return registry.AccountShared, registry.TierFree, true
	case "fallback":
		return registry.AccountService, registry.TierCheapest, true
	default:
		return "", "", false
	}
}

func knownAccessMethod(method string) bool {
	switch method {
	case "native-cc", "smithers-claude-p", "codex-cli", "agy-cli", "opencode-run", "gfp-keypool":
		return true
	default:
		return false
	}
}

func knownTaskClass(class string) bool {
	switch class {
	case "interactive-chat", "bulk-execution", "grunt", "taxonomy", "adversarial-cr",
		"final-gate", "sensitive", "background", "post-prompt":
		return true
	default:
		return false
	}
}

func intString(value int) string { return strconv.Itoa(value) }
