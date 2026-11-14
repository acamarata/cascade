package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/runtime"
)

// TestMCPProfileWideningIsLoosening verifies that compareMCPFamily reports a
// raise (compact -> compact-write -> full) as a loosening when
// config.accept_loosening is false, and that lowering is always free.
type mcpLooseningCase struct {
	name       string
	base       string
	next       string
	wantLoose  bool
	wantFamily string
	wantKey    string
}

func mcpLooseningCases() []mcpLooseningCase {
	return []mcpLooseningCase{
		{
			name:      "compact to compact-write is loosening",
			base:      `{"profile":"compact"}`,
			next:      `{"profile":"compact-write"}`,
			wantLoose: true,
		},
		{
			name:      "compact-write to full is loosening",
			base:      `{"profile":"compact-write"}`,
			next:      `{"profile":"full"}`,
			wantLoose: true,
		},
		{
			name:      "compact to full is loosening",
			base:      `{"profile":"compact"}`,
			next:      `{"profile":"full"}`,
			wantLoose: true,
		},
		{
			name:      "full to compact is not loosening",
			base:      `{"profile":"full"}`,
			next:      `{"profile":"compact"}`,
			wantLoose: false,
		},
		{
			name:      "full to compact-write is not loosening",
			base:      `{"profile":"full"}`,
			next:      `{"profile":"compact-write"}`,
			wantLoose: false,
		},
		{
			name:      "same profile is not loosening",
			base:      `{"profile":"compact-write"}`,
			next:      `{"profile":"compact-write"}`,
			wantLoose: false,
		},
		{
			name:      "empty base to compact is not loosening",
			base:      ``,
			next:      `{"profile":"compact"}`,
			wantLoose: false,
		},
		{
			name:      "empty base to compact-write is loosening",
			base:      ``,
			next:      `{"profile":"compact-write"}`,
			wantLoose: true,
		},
	}
}

func TestMCPProfileWideningIsLoosening(t *testing.T) {
	for _, tt := range mcpLooseningCases() {
		t.Run(tt.name, func(t *testing.T) {
			var base, next json.RawMessage
			if tt.base != "" {
				base = json.RawMessage(tt.base)
			}
			if tt.next != "" {
				next = json.RawMessage(tt.next)
			}

			paths, err := compareMCPFamily(base, next)
			if err != nil {
				t.Fatalf("compareMCPFamily error: %v", err)
			}

			if tt.wantLoose {
				if len(paths) == 0 {
					t.Errorf("expected loosening path, got none")
				} else if paths[0].Family != "mcp" {
					t.Errorf("expected family mcp, got %s", paths[0].Family)
				} else if paths[0].Key != "mcp.profile" {
					t.Errorf("expected key mcp.profile, got %s", paths[0].Key)
				}
			} else {
				if len(paths) != 0 {
					t.Errorf("expected no loosening, got %v", paths)
				}
			}
		})
	}
}

// TestAdmittedMCPProfileReturnsDefault verifies that admittedMCPProfile
// returns ProfileCompact when the family is absent or invalid.
type admittedMCPCase struct {
	name string
	eff  *runtime.EffectiveConfig
	want mcp.ToolProfile
}

func admittedMCPCases() []admittedMCPCase {
	return []admittedMCPCase{
		{
			name: "nil config",
			eff:  nil,
			want: mcp.ProfileCompact,
		},
		{
			name: "nil families",
			eff:  &runtime.EffectiveConfig{Families: nil},
			want: mcp.ProfileCompact,
		},
		{
			name: "missing mcp family",
			eff:  &runtime.EffectiveConfig{Families: map[string]json.RawMessage{}},
			want: mcp.ProfileCompact,
		},
		{
			name: "empty mcp family",
			eff:  &runtime.EffectiveConfig{Families: map[string]json.RawMessage{"mcp": nil}},
			want: mcp.ProfileCompact,
		},
		{
			name: "invalid json",
			eff:  &runtime.EffectiveConfig{Families: map[string]json.RawMessage{"mcp": json.RawMessage(`{invalid}`)}},
			want: mcp.ProfileCompact,
		},
		{
			name: "valid compact",
			eff:  &runtime.EffectiveConfig{Families: map[string]json.RawMessage{"mcp": json.RawMessage(`{"profile":"compact"}`)}},
			want: mcp.ProfileCompact,
		},
		{
			name: "valid compact-write",
			eff:  &runtime.EffectiveConfig{Families: map[string]json.RawMessage{"mcp": json.RawMessage(`{"profile":"compact-write"}`)}},
			want: mcp.ProfileCompactWrite,
		},
		{
			name: "valid full",
			eff:  &runtime.EffectiveConfig{Families: map[string]json.RawMessage{"mcp": json.RawMessage(`{"profile":"full"}`)}},
			want: mcp.ProfileFull,
		},
	}
}

func TestAdmittedMCPProfileReturnsDefault(t *testing.T) {
	for _, tt := range admittedMCPCases() {
		t.Run(tt.name, func(t *testing.T) {
			got := admittedMCPProfile(tt.eff)
			if got != tt.want {
				t.Errorf("admittedMCPProfile() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMCPRaiseReasonNamesToolCapabilities checks that a raise reports the
// per-tool capabilities it newly exposes.
func TestMCPRaiseReasonNamesToolCapabilities(t *testing.T) {
	paths, err := compareMCPFamily(nil, json.RawMessage(`{"profile":"compact-write"}`))
	if err != nil || len(paths) != 1 {
		t.Fatalf("compareMCPFamily = %v, %v", paths, err)
	}
	for _, tool := range mcp.CompactWriteToolNames {
		if !strings.Contains(paths[0].Reason, mcp.ToolCapability(tool)) {
			t.Errorf("reason %q lacks %s", paths[0].Reason, mcp.ToolCapability(tool))
		}
	}
	full, err := compareMCPFamily(json.RawMessage(`{"profile":"compact-write"}`), json.RawMessage(`{"profile":"full"}`))
	if err != nil || len(full) != 1 || !strings.Contains(full[0].Reason, "every registered tool") {
		t.Fatalf("full raise = %v, %v", full, err)
	}
	low, err := compareMCPFamily(json.RawMessage(`{"profile":"full"}`), json.RawMessage(`{"profile":"compact"}`))
	if err != nil || len(low) != 0 {
		t.Fatalf("lowering = %v, %v", low, err)
	}
}
