package antigravity

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestAntigravityArgvNeverSkipsPermissions(t *testing.T) {
	isolatedProbeHome(t)
	spec := provider.AgentJobSpec{Prompt: "Reply ok", Model: "test-v1", Effort: "high"}
	got, err := BuildArgv("/fixture/agy", provider.DriverDefaults{}, spec, time.Minute)
	want := []string{"/fixture/agy", "--print", "--output-format", "stream-json", "--mode", "plan",
		"--model", "test-v1", "--effort", "high", "--print-timeout", "1m0s", "Reply ok"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, %v; want %q", got, err, want)
	}
	for _, flag := range []string{"--dangerously-skip-permissions", "--skip-permissions", "--yolo", "--remote-control", "--mic-serve", "--unknown"} {
		for _, field := range []string{"SessionNameFlag", "KernelPromptFile", "SubagentKernelPromptFile", "PluginDir", "FallbackModel", "MCPConfigPath", "Effort", "KernelPrompt", "SubagentKernelPrompt"} {
			d := provider.DriverDefaults{}
			reflect.ValueOf(&d).Elem().FieldByName(field).SetString(flag)
			assertUnsafeArgv(t, d, spec)
		}
		for _, field := range []string{"Model", "Effort", "Prompt"} {
			bad := spec
			reflect.ValueOf(&bad).Elem().FieldByName(field).SetString(flag)
			assertUnsafeArgv(t, provider.DriverDefaults{}, bad)
		}
	}
	for _, value := range []string{"test --yolo", "test\x00", " test", "test\n--skip-permissions"} {
		bad := spec
		bad.Model = value
		assertUnsafeArgv(t, provider.DriverDefaults{}, bad)
	}
}

func assertUnsafeArgv(t *testing.T, defaults provider.DriverDefaults, spec provider.AgentJobSpec) {
	t.Helper()
	got, err := BuildArgv("/fixture/agy", defaults, spec, time.Minute)
	if got != nil || err != ErrUnsafeArgv || err.Error() != "invalid-input: antigravity: unsafe or unsupported launch argument" {
		t.Fatalf("unsafe argv = %q, %v", got, err)
	}
}

func TestAntigravityModeFromToolPolicy(t *testing.T) {
	isolatedProbeHome(t)
	for _, tc := range []struct{ policy, mode string }{
		{"", "plan"}, {provider.ToolPolicyNone, "plan"},
		{provider.ToolPolicyReadonly, "plan"}, {provider.ToolPolicyAll, "accept-edits"},
	} {
		spec := provider.AgentJobSpec{Prompt: "ok", ToolPolicy: tc.policy}
		argv, err := BuildArgv("agy", provider.DriverDefaults{}, spec, time.Minute)
		if err != nil || len(argv) != 9 || argv[4] != "--mode" || argv[5] != tc.mode {
			t.Errorf("policy %q: %q, %v", tc.policy, argv, err)
		}
	}
	argv, err := BuildArgv("agy", provider.DriverDefaults{}, provider.AgentJobSpec{ToolPolicy: "yolo"}, time.Minute)
	if argv != nil || err != provider.ErrInvalidToolPolicy || err.Error() != provider.ErrInvalidToolPolicy.Error() {
		t.Fatalf("invalid policy = %q, %v", argv, err)
	}
}

func TestAntigravitySandboxDecisionRecorded(t *testing.T) {
	isolatedProbeHome(t)
	readme, err := os.ReadFile("testdata/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"Sandbox emitted: no", "Auth source: none: fixture-only"} {
		if !strings.Contains(string(readme), decision) {
			t.Fatalf("README lacks %q", decision)
		}
	}
	for _, policy := range []string{"", "none", "readonly", "all"} {
		argv, err := BuildArgv("agy", provider.DriverDefaults{}, provider.AgentJobSpec{ToolPolicy: policy}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for _, arg := range argv {
			if arg == "--sandbox" {
				t.Fatalf("README says no sandbox: %q", argv)
			}
		}
	}
}

func TestAntigravityArgvTimeout(t *testing.T) {
	isolatedProbeHome(t)
	for _, tc := range []struct {
		configured, remaining time.Duration
		want                  string
	}{
		{0, time.Hour, "1m30s"}, {2 * time.Minute, time.Hour, "2m0s"},
		{time.Minute, 250 * time.Millisecond, "250ms"},
	} {
		d := provider.DriverDefaults{ApprovalTimeout: tc.configured, Effort: "low"}
		argv, err := BuildArgv("agy", d, provider.AgentJobSpec{Effort: "high"}, tc.remaining)
		if err != nil || argv[len(argv)-2] != tc.want || argv[7] != "high" {
			t.Fatalf("timeout %v = %q, %v", tc, argv, err)
		}
	}
	for _, remaining := range []time.Duration{0, -time.Second} {
		argv, err := BuildArgv("agy", provider.DriverDefaults{}, provider.AgentJobSpec{}, remaining)
		if argv != nil || err != ErrUnsafeArgv || err.Error() != ErrUnsafeArgv.Error() {
			t.Fatalf("expired = %q, %v", argv, err)
		}
	}
}
