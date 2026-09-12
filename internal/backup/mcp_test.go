// Purpose: unit tests for the MCP backup list registration and handler.
// Inputs: injected SnapshotListFunc, raw JSON input bytes.
// Outputs: verified tool name/grants, handler output shape, and input rejection.
// Constraints: no elevated verbs, no mutation operations, no real Store I/O.
// SPORT: internal.backup.mcp/ADD (P1-E19-W4-S42-T3).
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateListInput(t *testing.T) {
	cases := []struct {
		input   string
		wantErr bool
	}{
		{"", false},
		{"null", false},
		{"{}", false},
		{"   ", false},
		{`{"target":"foo"}`, true},
		{`{"filter":"x"}`, true},
	}
	for _, tc := range cases {
		err := validateListInput([]byte(tc.input))
		if (err != nil) != tc.wantErr {
			t.Errorf("validateListInput(%q): err=%v, wantErr=%v", tc.input, err, tc.wantErr)
		}
	}
}

func TestMCPRegistrationMetadata(t *testing.T) {
	reg := MCPRegistration(func(context.Context) ([]SnapshotSummary, error) { return nil, nil })
	if reg.Tool.Name != "cascade_backup_list" {
		t.Errorf("Tool.Name = %q, want cascade_backup_list", reg.Tool.Name)
	}
	if len(reg.Grants) == 0 || reg.Grants[0] != "read" {
		t.Errorf("Grants = %v, want [read]", reg.Grants)
	}
	if reg.Handler == nil {
		t.Fatal("Handler is nil")
	}
}

func TestMCPRegistrationHandlerSuccess(t *testing.T) {
	want := []SnapshotSummary{{ID: "snap-abc", Target: "main", ObjectCount: 3}}
	reg := MCPRegistration(func(_ context.Context) ([]SnapshotSummary, error) {
		return want, nil
	})
	out, err := reg.Handler(context.Background(), []byte("{}"))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	var result struct {
		Snapshots []SnapshotSummary `json:"snapshots"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Snapshots) != 1 || result.Snapshots[0].ID != "snap-abc" {
		t.Errorf("snapshots = %v", result.Snapshots)
	}
}

func TestMCPRegistrationHandlerEmpty(t *testing.T) {
	reg := MCPRegistration(func(_ context.Context) ([]SnapshotSummary, error) {
		return []SnapshotSummary{}, nil
	})
	out, err := reg.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handler with nil input: %v", err)
	}
	var result struct {
		Snapshots []SnapshotSummary `json:"snapshots"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Snapshots == nil {
		t.Error("snapshots field should be non-nil empty slice, got nil")
	}
}

func TestMCPRegistrationHandlerInvalidInput(t *testing.T) {
	reg := MCPRegistration(func(_ context.Context) ([]SnapshotSummary, error) { return nil, nil })
	_, err := reg.Handler(context.Background(), []byte(`{"filter":"x"}`))
	if err == nil {
		t.Fatal("non-empty input was accepted")
	}
}

func TestMCPRegistrationHandlerPropagatesError(t *testing.T) {
	want := errors.New("store unavailable")
	reg := MCPRegistration(func(_ context.Context) ([]SnapshotSummary, error) {
		return nil, want
	})
	_, err := reg.Handler(context.Background(), []byte("{}"))
	if err == nil {
		t.Fatal("handler did not propagate list error")
	}
}

// TestSnapshotIDFromManifestKey is a white-box test for the key parser used by
// listTargetSnapshots — it is the only internal path not exercised by the full
// integration tests.
func TestSnapshotIDFromManifestKey(t *testing.T) {
	cases := []struct {
		key  string
		want SnapshotID
		ok   bool
	}{
		{"manifests/snap-123.json", "snap-123", true},
		{"manifests/.json", "", false},
		{"other/snap.json", "", false},
		{"manifests/snap-123.yaml", "", false},
		{"manifests/", "", false},
	}
	for _, tc := range cases {
		got, ok := snapshotIDFromManifestKey(tc.key)
		if ok != tc.ok || got != tc.want {
			t.Errorf("snapshotIDFromManifestKey(%q) = (%q, %v), want (%q, %v)",
				tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

// TestParseVerifyInput covers cascade_backup_verify's request parsing:
// empty/null/{} select "" (single-target auto-select), a named target
// round-trips, and an unknown field refuses closed.
func TestParseVerifyInput(t *testing.T) {
	cases := []struct {
		input      string
		wantTarget string
		wantErr    bool
	}{
		{"", "", false},
		{"null", "", false},
		{"{}", "", false},
		{`{"target":"primary"}`, "primary", false},
		{`{"filter":"x"}`, "", true},
		{`not json`, "", true},
	}
	for _, tc := range cases {
		got, err := parseVerifyInput([]byte(tc.input))
		if (err != nil) != tc.wantErr {
			t.Errorf("parseVerifyInput(%q): err=%v, wantErr=%v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.wantTarget {
			t.Errorf("parseVerifyInput(%q) = %q, want %q", tc.input, got, tc.wantTarget)
		}
	}
}

func TestVerifyMCPRegistrationMetadata(t *testing.T) {
	reg := VerifyMCPRegistration(func(context.Context, string) (VerificationReport, error) {
		return VerificationReport{}, nil
	})
	if reg.Tool.Name != "cascade_backup_verify" {
		t.Errorf("Tool.Name = %q, want cascade_backup_verify", reg.Tool.Name)
	}
	if len(reg.Grants) == 0 || reg.Grants[0] != "read" {
		t.Errorf("Grants = %v, want [read]", reg.Grants)
	}
	if reg.Handler == nil {
		t.Fatal("Handler is nil")
	}
}

// TestVerifyMCPRegistrationHandlerSuccess proves the handler decodes the
// request's target, calls the injected run func with it, and returns the
// VerificationReport as the raw result -- the exact schema `backup verify
// --json` emits in its envelope's data field (this ticket's own
// acceptance criterion for MCP/CLI schema parity).
func TestVerifyMCPRegistrationHandlerSuccess(t *testing.T) {
	var gotTarget string
	want := VerificationReport{Target: "primary", Verified: true, CheckedChunks: 4}
	reg := VerifyMCPRegistration(func(_ context.Context, target string) (VerificationReport, error) {
		gotTarget = target
		return want, nil
	})
	out, err := reg.Handler(context.Background(), []byte(`{"target":"primary"}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if gotTarget != "primary" {
		t.Fatalf("run func received target %q, want %q", gotTarget, "primary")
	}
	var got VerificationReport
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("Handler output is not valid JSON: %v", err)
	}
	if got != want {
		t.Fatalf("Handler output = %+v, want %+v", got, want)
	}
}

// TestVerifyMCPRegistrationHandlerPropagatesError proves a run-func error
// (e.g. a real integrity-gate failure) is propagated, never swallowed.
func TestVerifyMCPRegistrationHandlerPropagatesError(t *testing.T) {
	reg := VerifyMCPRegistration(func(context.Context, string) (VerificationReport, error) {
		return VerificationReport{}, errors.New("injected verify failure")
	})
	if _, err := reg.Handler(context.Background(), []byte("{}")); err == nil {
		t.Fatal("handler did not propagate the run func's error")
	}
}
