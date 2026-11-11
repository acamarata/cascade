// Purpose: test `cascade provider exec` scrub-only and account modes.
// Inputs: a fake helper binary, a child that prints its environment.
// Outputs: assertions on what the child saw.
// Constraints: the child never sees the token; the test never runs the
//   operator's real helper or claude config.
// SPORT: cli.provider.exec/ADD (P1-GWY-10a, contract:gateway-operator-exec).

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestProviderExecScrubOnly verifies that without an account, the child
// sees none of HostInjectedVars() and HOME is preserved.
func TestProviderExecScrubOnly(t *testing.T) {
	// Create a child that prints its environment
	childCode := `package main
import (
	"fmt"
	"os"
)
func main() {
	for _, env := range os.Environ() {
		fmt.Println(env)
	}
}`
	childBin := writeHelper(t, "child_print_env", childCode)

	// Set host-injected vars that should be stripped
	t.Setenv("CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "1")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "host-token-canary")
	t.Setenv("ANTHROPIC_BASE_URL", "https://host.example.com")

	// Preserve HOME
	home := os.Getenv("HOME")
	t.Setenv("HOME", home)

	// Run: cascade provider exec -- childBin. The exec step runs the child
	// as a subprocess here: syscall.Exec would replace this test binary.
	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	gotArgv := captureExec(t, &stdout)

	err := runProviderExec(cmd, []string{"--", childBin})
	if err != nil {
		t.Fatalf("runProviderExec failed: %v", err)
	}

	output := stdout.String()
	if len(*gotArgv) == 0 || (*gotArgv)[0] != childBin {
		t.Fatalf("exec argv = %q, want argv[0] %q", *gotArgv, childBin)
	}

	// Verify host-injected vars are gone
	if strings.Contains(output, "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST=") {
		t.Error("child saw CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST, expected it stripped")
	}
	if strings.Contains(output, "ANTHROPIC_AUTH_TOKEN=") {
		t.Error("child saw ANTHROPIC_AUTH_TOKEN, expected it stripped")
	}
	if strings.Contains(output, "ANTHROPIC_BASE_URL=") {
		t.Error("child saw ANTHROPIC_BASE_URL, expected it stripped")
	}

	// Verify HOME is preserved
	if !strings.Contains(output, "HOME="+home) {
		t.Errorf("child did not see HOME=%s, expected it preserved", home)
	}
}

// TestProviderExecInjectsGateway verifies that with an account, the child
// sees the account's gateway variables and token.
func TestProviderExecInjectsGateway(t *testing.T) {
	// This test will be implemented after the main command
	t.Skip("implementation pending")
}

// TestProviderExecRefusals verifies unknown account, changed helper digest,
// and missing -- are refused.
func TestProviderExecRefusals(t *testing.T) {
	// This test will be implemented after the main command
	t.Skip("implementation pending")
}

// TestProviderExecHelperFailureStartsNothing verifies that a helper that
// exits 1 starts nothing.
func TestProviderExecHelperFailureStartsNothing(t *testing.T) {
	// This test will be implemented after the main command
	t.Skip("implementation pending")
}

// writeHelper writes a Go source file, compiles it, and returns the binary path.
func writeHelper(t *testing.T, name, code string) string {
	t.Helper()
	dir := t.TempDir()
	srcPath := filepath.Join(dir, name+".go")
	if err := os.WriteFile(srcPath, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		binPath += ".exe" // exec.LookPath on Windows finds only names with a PATHEXT suffix
	}
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build helper: %v\n%s", err, out)
	}
	return binPath
}

// captureExec replaces platformExecFn for one test with a runner that starts
// the child as a subprocess and writes its output to out: syscall.Exec would
// replace the test binary itself. It returns where the child's argv lands.
func captureExec(t *testing.T, out *bytes.Buffer) *[]string {
	t.Helper()
	var argv []string
	orig := platformExecFn
	t.Cleanup(func() { platformExecFn = orig })
	platformExecFn = func(path string, args []string, env []string) error {
		argv = args
		child := exec.Command(path, args[1:]...)
		child.Env = env
		child.Stdout = out
		child.Stderr = out
		return child.Run()
	}
	return &argv
}
