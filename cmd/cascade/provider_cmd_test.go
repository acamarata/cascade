// Purpose: CLI-surface tests for `cascade provider add` (P1-E10-W3-S20-T1):
//   flag mutual-exclusivity, the CASCADE_NO_INPUT gate on --key, the
//   --key-env non-interactive path end to end, and the --help automation-
//   parity acceptance criterion. Every test substitutes a fake Doer and an
//   isolated MemoryRegistry (providerDeps.Doer/Registry), so no test opens
//   a socket or touches the real keychain. provider_add_list_test.go
//   covers the add/list/remove unification fix against the real durable
//   store (deps.Registry left nil, matching production).
// SPORT: cli.provider.add/ADD (P1-E10-W3-S20-T1).

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
)

// fakeProviderDoer is a recording, in-memory intake.Doer.
type fakeProviderDoer struct {
	responses map[string]intake.HTTPResponse
}

func (f fakeProviderDoer) Do(_ context.Context, req intake.HTTPRequest) (intake.HTTPResponse, error) {
	if resp, ok := f.responses[req.URL]; ok {
		return resp, nil
	}
	return intake.HTTPResponse{Status: 404, Body: []byte("not found")}, nil
}

// anthropicModelsOnlyDoer answers the shape-probe leg only; every test here
// runs with --no-verify so the live micro-verify call is never made.
func anthropicModelsOnlyDoer() intake.Doer {
	return fakeProviderDoer{responses: map[string]intake.HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 200, Body: []byte(
			`{"data":[{"id":"claude-3-5-sonnet-20241022","type":"model"}]}`)},
	}}
}

// testProviderDeps builds providerDeps over a file vault in t.TempDir(), a
// fake Doer, and a fresh, isolated MemoryRegistry.
func testProviderDeps(t *testing.T, env map[string]string) providerDeps {
	t.Helper()
	dir := t.TempDir()
	homeDir := t.TempDir()
	paths, err := runtime.NewPathProvider(
		func(k string) string {
			if k == "CASCADE_HOME" {
				return homeDir
			}
			return env[k]
		},
		func() (string, error) { return homeDir, nil },
	)
	if err != nil {
		t.Fatalf("runtime.NewPathProvider: %v", err)
	}
	return providerDeps{
		Paths:  paths,
		Getenv: func(k string) string { return env[k] },
		NewCustody: func() (secrets.Custody, error) {
			return secrets.SelectCustody(secrets.Config{
				Service: "cascade-provider-cli-test", Dir: dir,
				Passphrase: "cli-test-pass", Runner: alwaysFailRunner,
			})
		},
		Gate:         okGate{},
		ReadStdin:    func() ([]byte, error) { return []byte("sk-ant-test-value\n"), nil },
		StdinIsPiped: func() bool { return true },
		Doer:         anthropicModelsOnlyDoer(),
		Registry:     intake.NewMemoryRegistry(),
	}
}

// runProvider executes one `provider ...` command against an isolated
// tree and returns stdout, stderr and the error.
func runProvider(t *testing.T, deps providerDeps, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cascade", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().Bool("quiet", false, "")
	root.PersistentFlags().Bool("verbose", false, "")
	root.PersistentFlags().Bool("no-color", false, "")
	cmd := newProviderCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"provider"}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestProviderAddRequiresExactlyOneCredentialSource(t *testing.T) {
	deps := testProviderDeps(t, nil)
	if _, _, err := runProvider(t, deps, "add", "p1"); err == nil {
		t.Fatal("expected an error when no credential flag is set")
	}
	if _, _, err := runProvider(t, deps, "add", "p1", "--key", "--oauth"); err == nil {
		t.Fatal("expected an error when two credential flags are set")
	}
}

func TestProviderAddKeyPathEndToEnd(t *testing.T) {
	deps := testProviderDeps(t, nil)
	stdout, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify", "--json")
	if err != nil {
		t.Fatalf("provider add --key: %v", err)
	}
	if !strings.Contains(stdout, "converged") {
		t.Fatalf("expected a converged status in --json output, got %q", stdout)
	}
	if strings.Contains(stdout, "sk-ant-test-value") {
		t.Fatal("the raw credential value leaked into --json output")
	}
}

func TestProviderAddKeyEnvNonInteractive(t *testing.T) {
	deps := testProviderDeps(t, map[string]string{"MY_TEST_KEY": "sk-ant-test-value"})
	stdout, _, err := runProvider(t, deps, "add", "myclaude", "--key-env", "MY_TEST_KEY", "--no-verify", "--json")
	if err != nil {
		t.Fatalf("provider add --key-env: %v", err)
	}
	if !strings.Contains(stdout, "converged") {
		t.Fatalf("expected a converged status, got %q", stdout)
	}
}

func TestProviderAddKeyIdempotentAcrossInvocations(t *testing.T) {
	deps := testProviderDeps(t, nil)
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	stdout, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify", "--json")
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if !strings.Contains(stdout, "updated") {
		t.Fatalf("expected the re-add to report updated, got %q", stdout)
	}
}

func TestProviderAddKeyCASCADENoInputRefusesTTY(t *testing.T) {
	deps := testProviderDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	deps.StdinIsPiped = func() bool { return false }
	_, _, err := runProvider(t, deps, "add", "myclaude", "--key")
	if err == nil {
		t.Fatal("expected CASCADE_NO_INPUT=1 to refuse --key on an interactive TTY")
	}
	if !strings.Contains(err.Error(), "--key-env") {
		t.Errorf("expected the refusal to cite --key-env, got %v", err)
	}
}

func TestProviderAddKeyCASCADENoInputAllowsPipedStdin(t *testing.T) {
	// Piped stdin is not an interactive prompt: CASCADE_NO_INPUT must not
	// refuse a script that pipes the key in (automation parity, §5.8).
	deps := testProviderDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	if _, _, err := runProvider(t, deps, "add", "myclaude", "--key", "--no-verify"); err != nil {
		t.Fatalf("expected piped --key to succeed under CASCADE_NO_INPUT=1, got %v", err)
	}
}

func TestProviderAddHelpDocumentsAllFlags(t *testing.T) {
	deps := testProviderDeps(t, nil)
	stdout, _, err := runProvider(t, deps, "add", "--help")
	if err != nil {
		t.Fatalf("provider add --help: %v", err)
	}
	for _, want := range []string{"--key", "--key-env", "--oauth", "--base-url", "--no-verify", "--pool", "CASCADE_NO_INPUT"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("provider add --help does not document %q:\n%s", want, stdout)
		}
	}
}

func TestNewProviderCmdProductionDepsConstructsCleanly(t *testing.T) {
	// Building the command tree must never touch the environment
	// (mirrors mountVaultCmd's own contract) - this only constructs the
	// tree, it never calls RunE.
	cmd := newProviderCmd(productionProviderDeps())
	if cmd.Use != "provider" {
		t.Fatalf("unexpected Use: %q", cmd.Use)
	}
	found := false
	for _, sub := range cmd.Commands() {
		if strings.HasPrefix(sub.Use, "add") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an `add` subcommand to be mounted")
	}
}

func TestBuildIntakeDepsPropagatesCustodyError(t *testing.T) {
	deps := providerDeps{
		NewCustody: func() (secrets.Custody, error) {
			return nil, os.ErrPermission
		},
	}
	if _, err := buildIntakeDeps(deps, intake.NewMemoryRegistry()); err == nil {
		t.Fatal("expected buildIntakeDeps to propagate a custody construction error")
	}
}

func TestProductionProviderDepsResolvesDataDir(t *testing.T) {
	// Smoke-checks that productionProviderDeps builds without panicking
	// and that its lazily-resolved NewCustody path is reachable; it does
	// not assert a specific backend, since that varies by host. Registry
	// is deliberately nil here: resolveProviderRegistry opens the durable
	// store lazily, at `add` invocation time, never at Deps construction
	// (constructing the command tree must never touch disk).
	deps := productionProviderDeps()
	if deps.NewCustody == nil || deps.Doer == nil {
		t.Fatal("expected productionProviderDeps to populate every required field")
	}
	if deps.Registry != nil {
		t.Fatal("expected productionProviderDeps to leave Registry nil for lazy resolution")
	}
}
