package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Purpose: the mcp_integration framework-shipped check (ticket contract
//
//	task 7a) — closes v1's exact false-green failure mode: a harness's
//	MCP server registration existing on paper while the command path
//	does not resolve, or resolves to a binary that never answers a
//	protocol request.
//
// Inputs: a HarnessDiscoverer (injected — real harness-config discovery
//
//	is P/S-34's; this ticket wires the interface at the composition
//	root and tests against fixtures per the contract) and a
//	stdioRoundTrip function (defaults to a real os/exec-based JSON-RPC
//	round trip; tests inject one pointed at a local fake stdio server
//	binary under t.TempDir()).
//
// Outputs: CheckResult — status=error names the specific harness config
//
//	path and which step failed (registration/command-resolve/round-trip)
//	when any harness fails any step.
//
// Constraints: no network calls (Art.7.2) — the round trip is a local
//
//	subprocess over stdio, never a socket. context.Context bounds the
//	subprocess lifetime.
//
// SPORT: placeholder: doctor/framework (ADD).

// HarnessConfig describes one detected harness's MCP server registration,
// as DiscoverHarnessConfigs reports it.
type HarnessConfig struct {
	// ConfigPath is the harness config file this registration was read
	// from (named in every failing CheckResult).
	ConfigPath string
	// ServerName is the registered MCP server's name; empty means the
	// harness config exists but carries no registration at all
	// (registration-missing failure).
	ServerName string
	// Command is the registered server's launch command (may be a bare
	// name to resolve via PATH, or an absolute/relative path).
	Command string
	// Args are the launch arguments.
	Args []string
}

// HarnessDiscoverer discovers every harness config on the host relevant
// to `cascade doctor`'s mcp_integration check. Injected so this check
// stays testable against fixtures without a live harness installation.
type HarnessDiscoverer interface {
	DiscoverHarnessConfigs(ctx context.Context) ([]HarnessConfig, error)
}

// stdioRoundTripFunc performs a live tools/list JSON-RPC request against
// a launched MCP server and returns nil only if a well-formed response
// arrives before ctx is done.
type stdioRoundTripFunc func(ctx context.Context, command string, args []string) error

// mcpIntegrationCheck implements Check for the mcp_integration probe.
type mcpIntegrationCheck struct {
	discover  HarnessDiscoverer
	roundTrip stdioRoundTripFunc
}

// NewMCPIntegrationCheck builds the mcp_integration Check. roundTrip may
// be nil to use the real os/exec-based round trip (production); tests
// pass one pointed at a fixture binary.
func NewMCPIntegrationCheck(discover HarnessDiscoverer, roundTrip stdioRoundTripFunc) Check {
	if roundTrip == nil {
		roundTrip = defaultStdioRoundTrip
	}
	return &mcpIntegrationCheck{discover: discover, roundTrip: roundTrip}
}

func (c *mcpIntegrationCheck) Name() string { return "mcp_integration" }

func (c *mcpIntegrationCheck) Describe() string {
	return "verifies each detected MCP harness registration, command resolution, and a live tools/list round-trip"
}

func (c *mcpIntegrationCheck) Metadata() CheckMeta {
	return CheckMeta{FirstRun: true, Fixable: false}
}

func (c *mcpIntegrationCheck) Fix(context.Context) (FixResult, error) {
	return FixResult{}, ErrCheckNotFixable
}

// Run discovers every harness config and asserts, per config: (a) a
// registration is present, (b) its command resolves to an existing
// executable, (c) a live stdio round trip answers tools/list.
func (c *mcpIntegrationCheck) Run(ctx context.Context) (CheckResult, error) {
	configs, err := c.discover.DiscoverHarnessConfigs(ctx)
	if err != nil {
		return CheckResult{Status: StatusError, Message: "harness discovery failed", Detail: err.Error()}, nil
	}
	if len(configs) == 0 {
		return CheckResult{Status: StatusOK, Message: "no MCP harness configs detected"}, nil
	}

	var failures []string
	for _, cfg := range configs {
		if f := c.checkOneHarness(ctx, cfg); f != "" {
			failures = append(failures, f)
		}
	}
	if len(failures) > 0 {
		return CheckResult{
			Status:  StatusError,
			Message: fmt.Sprintf("%d/%d MCP harness config(s) failing", len(failures), len(configs)),
			Detail:  strings.Join(failures, "; "),
		}, nil
	}
	return CheckResult{Status: StatusOK, Message: fmt.Sprintf("%d MCP harness config(s) verified", len(configs))}, nil
}

// checkOneHarness runs the three-step assertion for one HarnessConfig,
// returning a failure description naming cfg.ConfigPath and the failing
// step, or "" when all three steps pass.
func (c *mcpIntegrationCheck) checkOneHarness(ctx context.Context, cfg HarnessConfig) string {
	if cfg.ServerName == "" {
		return fmt.Sprintf("%s: registration missing", cfg.ConfigPath)
	}
	resolved, err := resolveCommandPath(cfg.Command)
	if err != nil {
		return fmt.Sprintf("%s: command %q does not resolve: %v", cfg.ConfigPath, cfg.Command, err)
	}
	if err := c.roundTrip(ctx, resolved, cfg.Args); err != nil {
		return fmt.Sprintf("%s: tools/list round-trip failed: %v", cfg.ConfigPath, err)
	}
	return ""
}

// resolveCommandPath resolves command to an existing executable: an
// absolute/relative path is os.Stat'd directly, a bare name is resolved
// via PATH lookup.
func resolveCommandPath(command string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("empty command")
	}
	if strings.ContainsRune(command, filepath.Separator) {
		if _, err := os.Stat(command); err != nil {
			return "", err
		}
		return command, nil
	}
	return exec.LookPath(command)
}

// toolsListRequest is the minimal JSON-RPC 2.0 tools/list request body.
const toolsListRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"

// defaultStdioRoundTrip launches command (a resolved executable path)
// with args, writes one tools/list JSON-RPC request to its stdin, and
// reads one line of response from stdout, verifying it decodes as a
// JSON-RPC response with no top-level "error" member. It never touches
// the network — command runs as a local subprocess only.
func defaultStdioRoundTrip(ctx context.Context, command string, args []string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	if _, err := stdin.Write([]byte(toolsListRequest)); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		return fmt.Errorf("no response before EOF")
	}
	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("server returned error: %s", resp.Error.Message)
	}
	return nil
}
