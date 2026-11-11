package antigravity

// Purpose: construct the closed, permission-safe print invocation.
// Inputs: binary, driver defaults, job and caller-bounded remaining time.
// Outputs: exact argv or a frozen-kind refusal before spawning.
// Constraints: no additional flags, permission bypass or listener mode.
// SPORT: providers.agents.antigravity/ADD (P1-AGT-07c).

import (
	"regexp"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrUnsafeArgv refuses unsupported defaults and flag-shaped values.
var ErrUnsafeArgv = cascade.New(cascade.KindInvalidInput, "antigravity: unsafe or unsupported launch argument")

const defaultPrintTimeout = 90 * time.Second

// sandboxPrintVerified records the decision in testdata/README.md.
const sandboxPrintVerified = false

var plainArgument = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// BuildArgv builds the one supported print invocation. remaining must be
// the positive envelope lifetime calculated with the caller's injected
// clock. ApprovalTimeout supplies the configured timeout (zero uses 90s);
// the smaller of that timeout and remaining is emitted without rounding.
func BuildArgv(binary string, defaults provider.DriverDefaults, spec provider.AgentJobSpec, remaining time.Duration) ([]string, error) {
	if binary == "" || strings.HasPrefix(binary, "-") || remaining <= 0 {
		return nil, ErrUnsafeArgv
	}
	if err := checkDefaults(defaults); err != nil {
		return nil, err
	}
	mode, err := modeFromToolPolicy(spec.ToolPolicy)
	if err != nil {
		return nil, err
	}
	effort := spec.Effort
	if effort == "" {
		effort = defaults.Effort
	}
	if !safeOptionalArgument(spec.Model) || !safeOptionalArgument(effort) || strings.HasPrefix(strings.TrimSpace(spec.Prompt), "-") || strings.ContainsRune(spec.Prompt, 0) {
		return nil, ErrUnsafeArgv
	}
	timeout := defaults.ApprovalTimeout
	if timeout == 0 {
		timeout = defaultPrintTimeout
	}
	if remaining < timeout {
		timeout = remaining
	}
	argv := []string{binary, "--print", "--output-format", "stream-json", "--mode", mode}
	if sandboxPrintVerified && mode == "plan" {
		argv = append(argv, "--sandbox")
	}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	if effort != "" {
		argv = append(argv, "--effort", effort)
	}
	return append(argv, "--print-timeout", timeout.String(), spec.Prompt), nil
}

func modeFromToolPolicy(policy string) (string, error) {
	resolved, err := provider.ResolveToolPolicy(policy)
	if err != nil {
		return "", err
	}
	if resolved == provider.ToolPolicyAll {
		return "accept-edits", nil
	}
	return "plan", nil
}

func safeOptionalArgument(value string) bool {
	return value == "" || plainArgument.MatchString(value)
}

// checkDefaults rejects every unsupported default instead of forwarding
// arbitrary flags. Only effort and the timeout affect this invocation.
func checkDefaults(d provider.DriverDefaults) error {
	values := []string{d.SessionNameFlag, d.KernelPromptFile, d.SubagentKernelPromptFile,
		d.PluginDir, d.FallbackModel, d.MCPConfigPath, d.KernelPrompt, d.SubagentKernelPrompt}
	for _, value := range values {
		if value != "" {
			return ErrUnsafeArgv
		}
	}
	if !safeOptionalArgument(d.Effort) || d.MaxTurns != 0 || d.MaxBudgetUSD != 0 || d.ApprovalTimeout < 0 {
		return ErrUnsafeArgv
	}
	return nil
}
