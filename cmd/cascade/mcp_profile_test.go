//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func mcpEff(profile string) *runtime.EffectiveConfig {
	if profile == "" {
		return &runtime.EffectiveConfig{}
	}
	return &runtime.EffectiveConfig{Families: map[string]json.RawMessage{
		"mcp": json.RawMessage(`{"profile":"` + profile + `"}`)}}
}

type serveCase struct {
	name     string
	admitted string
	flag     string
	raise    ProfileRaiseChecker
	want     mcp.ToolProfile
	wantNote bool
}

func serveCases() []serveCase {
	allow := ProfileRaiseChecker(func(context.Context, mcp.ToolProfile) error { return nil })
	deny := ProfileRaiseChecker(func(context.Context, mcp.ToolProfile) error { return cascade.ErrPolicyDenied })
	boom := ProfileRaiseChecker(func(context.Context, mcp.ToolProfile) error { return errors.New("daemon down") })
	onlyCW := ProfileRaiseChecker(func(_ context.Context, p mcp.ToolProfile) error {
		if p == mcp.ProfileCompactWrite {
			return nil
		}
		return cascade.ErrPolicyDenied
	})
	return []serveCase{
		{"full no token", "", "full", nil, mcp.ProfileCompact, true},
		{"compact-write no token", "", "compact-write", nil, mcp.ProfileCompact, true},
		{"deny", "", "compact-write", deny, mcp.ProfileCompact, true},
		{"error", "", "full", boom, mcp.ProfileCompact, true},
		{"standing grant", "", "compact-write", onlyCW, mcp.ProfileCompactWrite, false},
		{"grant of one is not the other", "", "full", onlyCW, mcp.ProfileCompact, true},
		{"allow raises", "", "full", allow, mcp.ProfileFull, false},
		{"no flag serves admitted", "compact-write", "", nil, mcp.ProfileCompactWrite, false},
		{"lowering is free", "full", "compact", nil, mcp.ProfileCompact, false},
		{"unknown flag is compact", "", "FULL", nil, mcp.ProfileCompact, false},
	}
}

func TestMCPServeFlagCannotWidenPastAdmitted(t *testing.T) {
	for _, tt := range serveCases() {
		t.Run(tt.name, func(t *testing.T) {
			got, note := resolveServeProfile(context.Background(), tt.flag, mcpEff(tt.admitted), tt.raise)
			if got != tt.want {
				t.Fatalf("profile = %q, want %q", got, tt.want)
			}
			if (note != "") != tt.wantNote {
				t.Fatalf("note = %q, wantNote %v", note, tt.wantNote)
			}
			wantNote := "cascade: --profile " + tt.flag + " exceeds the admitted [mcp].profile compact; serving compact"
			if tt.wantNote && note != wantNote {
				t.Fatalf("note = %q, want %q", note, wantNote)
			}
		})
	}
}

func TestProfileToolNamesResolveAliases(t *testing.T) {
	if !profileToolVisible(mcp.ProfileCompact, "cascade_search") || !profileToolVisible(mcp.ProfileCompact, "cascade_context_search") {
		t.Fatal("compact must offer the search alias and its target")
	}
	if profileToolVisible(mcp.ProfileCompact, "cascade_memory_remember") {
		t.Fatal("compact offers a mutating tool")
	}
	if !profileToolVisible(mcp.ProfileCompactWrite, "cascade_memory_remember") {
		t.Fatal("compact-write misses a write member")
	}
	if profileToolNames(mcp.ProfileFull) != nil || !profileToolVisible(mcp.ProfileFull, "anything") {
		t.Fatal("full has no member filter")
	}
}

func TestExecutiveServeConfigIsCompactWrite(t *testing.T) {
	got, err := executiveServeConfig("/usr/local/bin/cascade")
	if err != nil || !strings.Contains(string(got), `"--profile","compact-write"`) {
		t.Fatalf("config = %s, %v", got, err)
	}
	if _, err := executiveServeConfig("cascade"); !errors.Is(err, cascade.ErrInvalidInput) {
		t.Fatalf("relative bin err = %v", err)
	}
}

func mintToken(t *testing.T, f capabilityCheckFixture, id string) string {
	t.Helper()
	tok, err := f.minter.Mint(principal.Principal{ID: id, Kind: principal.KindMCP, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatalf("mint %s: %v", id, err)
	}
	return tok
}

// TestPolicyCheckersDialCallerPrincipal dials a daemon registry over a real
// unix socket with A's and B's tokens: A holds the grants, B holds none.
func TestPolicyCheckersDialCallerPrincipal(t *testing.T) {
	f := newCapabilityCheckFixture(t)
	a := policy.Subject{Kind: policy.SubjectAgent, ID: "principal-a"}
	f.grant(t, a, "mcp.profile.compact-write")
	f.grant(t, a, mcp.ToolCapability("cascade_memory_remember"))
	sock := serveForTest(t, rpc.NewHandler(f.reg), rpc.ConnContext)
	ctx := context.Background()
	for who, tc := range map[string]struct {
		token   string
		allowed bool
	}{"A": {mintToken(t, f, "principal-a"), true}, "B": {mintToken(t, f, "principal-b"), false}} {
		token := tc.token
		dial := func() (*client.Client, error) { return dialTo(sock)(token) }
		toolErr := policyToolChecker(dial).CheckTool(ctx, "cascade_memory_remember")
		raise := newProfileRaiseChecker(fixedEnv(token), dialTo(sock))
		raiseErr := raise(ctx, mcp.ProfileCompactWrite)
		if (toolErr == nil) != tc.allowed || (raiseErr == nil) != tc.allowed {
			t.Fatalf("%s: tool err %v, raise err %v, want allowed=%v", who, toolErr, raiseErr, tc.allowed)
		}
		if err := raise(ctx, mcp.ProfileFull); err == nil {
			t.Fatalf("%s: raise to full allowed without a grant", who)
		}
	}
}

func TestPolicyCheckersFailClosed(t *testing.T) {
	ctx := context.Background()
	down := func() (*client.Client, error) { return nil, cascade.New(cascade.KindUnavailable, "down") }
	if err := policyToolChecker(down).CheckTool(ctx, "cascade_ci_run"); err == nil {
		t.Fatal("unreachable daemon allowed a tool call")
	}
	if err := policyToolChecker(nil).CheckTool(ctx, "cascade_ci_run"); err == nil {
		t.Fatal("nil dialer allowed a tool call")
	}
	if newProfileRaiseChecker(fixedEnv(""), dialTo("x")) != nil ||
		newProfileRaiseChecker(fixedEnv(emptyTokenMarker), dialTo("x")) != nil {
		t.Fatal("a tokenless process got a raise checker")
	}
}

// TestMCPServeProfileFlagServesNarrowerTools drives `mcp serve --profile full`
// with [mcp] unset and no token: the served tools/list holds the compact
// set only, and a write tool is refused as unknown.
func TestMCPServeProfileFlagServesNarrowerTools(t *testing.T) {
	read := mcp.CoreRegistration{Tool: mcp.Tool{Name: "cascade_context_search", Description: "r"}, Grants: []string{"read"},
		Handler: func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil }}
	write := mcp.CoreRegistration{Tool: mcp.Tool{Name: "cascade_memory_remember", Description: "w"}, Grants: []string{"read"},
		Handler: func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil }}
	other := mcp.CoreRegistration{Tool: mcp.Tool{Name: "cascade_other_full_only", Description: "f"}, Grants: []string{"read"},
		Handler: func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil }}
	tools := mcp.NewToolRegistry(func() []plugin.BuiltinRegistration { return nil }, mcp.AllowAllFilter{}, read, write, other)
	deps := mcpDeps{
		StdinIsPipe: func() bool { return true },
		ServeStdio: func(ctx context.Context, in io.Reader, out io.Writer) error {
			return serveStdioReal(ctx, tools, in, out)
		},
	}
	cmd := newMCPServeCmd(deps)
	frames := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cascade_memory_remember","arguments":{}}}` + "\n"
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(frames))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--stdio", "--profile", "full"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "cascade_context_search") || strings.Contains(got, "cascade_other_full_only") {
		t.Fatalf("tools/list = %s, want compact members only", got)
	}
	if !strings.Contains(got, "unknown tool: cascade_memory_remember") {
		t.Fatalf("write tool call not refused as unknown: %s", got)
	}
	if want := "cascade: --profile full exceeds the admitted [mcp].profile compact; serving compact"; !strings.Contains(errOut.String(), want) {
		t.Fatalf("stderr = %q, want %q", errOut.String(), want)
	}
}
