// Purpose: prove `provider exec <account>` hands the child the account's
// gateway base URL and token.
// Inputs: a fixture config with one gateway account, a child printing its env.
// Outputs: assertions on what the child saw.
// Constraints: the token comes from the account's credential source, never
// from the parent's ANTHROPIC_* variables.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestProviderExecInjectsGatewayAccount runs a child on a configured account.
func TestProviderExecInjectsGatewayAccount(t *testing.T) {
	child := writeHelper(t, "child_print_env", `package main
import (
	"fmt"
	"os"
)
func main() {
	for _, e := range os.Environ() {
		fmt.Println(e)
	}
}`)
	deps := testProviderDeps(t, map[string]string{"GATEWAY_TOKEN": "acct-token-canary"})
	if err := os.MkdirAll(filepath.Dir(deps.Paths.ConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[gateways.acct]\nbase_url = \"https://gateway.invalid\"\nkey_env = \"GATEWAY_TOKEN\"\n"
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := execProviderDeps
	execProviderDeps = func() providerDeps { return deps }
	t.Cleanup(func() { execProviderDeps = orig })
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "parent-token-canary")
	t.Setenv("ANTHROPIC_BASE_URL", "https://parent.invalid")

	var out bytes.Buffer
	cmd := &cobra.Command{}
	captureExec(t, &out)
	if err := runProviderExec(cmd, []string{"acct", "--", child}); err != nil {
		t.Fatalf("runProviderExec: %v", err)
	}
	got := out.String()
	for _, want := range []string{"ANTHROPIC_BASE_URL=https://gateway.invalid\n", "ANTHROPIC_AUTH_TOKEN=acct-token-canary\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("child env lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "parent-token-canary") || strings.Contains(got, "parent.invalid") {
		t.Errorf("child kept a parent gateway variable:\n%s", got)
	}
}

// TestProviderExecUnknownGatewayAccount refuses an account that is not configured.
func TestProviderExecUnknownGatewayAccount(t *testing.T) {
	deps := testProviderDeps(t, map[string]string{})
	orig := execProviderDeps
	execProviderDeps = func() providerDeps { return deps }
	t.Cleanup(func() { execProviderDeps = orig })
	var out bytes.Buffer
	captureExec(t, &out)
	if err := runProviderExec(&cobra.Command{}, []string{"nope", "--", "true"}); err == nil {
		t.Fatal("unknown account started a child")
	}
	if out.Len() != 0 {
		t.Fatalf("child ran: %s", out.String())
	}
}
