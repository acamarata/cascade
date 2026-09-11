package daemon

// Purpose: unit coverage for conductorExecuteParams.toModelRequest and
//   parseSensitivityTier as DECODERS: asserts the real field mapping and,
//   specifically, the fail-closed sensitivity path this file's own doc
//   comment promises - an unrecognised or empty sensitivity name must
//   resolve to the most restrictive tier, never a permissive default.
// SPORT: internal/daemon (ADD, coverage-floor fix).

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestParseSensitivityTier_EmptyFailsClosedToRestricted proves the
// documented zero-value contract: an absent sensitivity name resolves to
// SensitivityRestricted, the most restrictive tier - never
// SensitivityPublic or any other permissive default.
func TestParseSensitivityTier_EmptyFailsClosedToRestricted(t *testing.T) {
	tier, err := parseSensitivityTier("")
	if err != nil {
		t.Fatalf("parseSensitivityTier(\"\"): unexpected error %v", err)
	}
	if tier != provider.SensitivityRestricted {
		t.Fatalf("parseSensitivityTier(\"\") = %v, want SensitivityRestricted (the fail-closed default)", tier)
	}
}

// TestParseSensitivityTier_UnknownFailsClosed proves the security
// property this file's own comment names: an unrecognised wire name is a
// KindInvalidInput error, and specifically NEVER silently resolved to
// SensitivityPublic or any tier more permissive than Restricted.
func TestParseSensitivityTier_UnknownFailsClosed(t *testing.T) {
	tier, err := parseSensitivityTier("nonexistent-tier")
	if err == nil {
		t.Fatalf("parseSensitivityTier(\"nonexistent-tier\") = %v, nil, want a KindInvalidInput error", tier)
	}
	if tier > provider.SensitivityRestricted {
		t.Fatalf("parseSensitivityTier on unknown input returned tier %v, want the zero (most restrictive) value on the error path", tier)
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("parseSensitivityTier error kind: got %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "nonexistent-tier") {
		t.Errorf("parseSensitivityTier error = %q, want it to name the rejected value", err.Error())
	}
}

// TestParseSensitivityTier_RoundTripsEveryDeclaredTier proves every
// declared provider.SensitivityTier name parses back to its own tier - the
// same taxonomy table doc comment's "no second, driftable string table"
// claim, checked against the SDK's own String() rather than a second copy.
func TestParseSensitivityTier_RoundTripsEveryDeclaredTier(t *testing.T) {
	for _, want := range sensitivityTiers {
		got, err := parseSensitivityTier(want.String())
		if err != nil {
			t.Fatalf("parseSensitivityTier(%q): unexpected error %v", want.String(), err)
		}
		if got != want {
			t.Errorf("parseSensitivityTier(%q) = %v, want %v", want.String(), got, want)
		}
	}
}

// TestConductorExecuteParams_ToModelRequest_MapsEveryField proves the
// field-for-field mapping toModelRequest's own doc comment promises,
// including sensitivity resolving through parseSensitivityTier rather
// than being copied verbatim.
func TestConductorExecuteParams_ToModelRequest_MapsEveryField(t *testing.T) {
	p := conductorExecuteParams{
		TaskID:    "task-42",
		TaskClass: "chat",
		Inputs: []conductorExecuteMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
		},
		Requirements: provider.Requirements{Context: 4096},
		Sensitivity:  provider.SensitivityInternal.String(),
		FanOut:       2,
	}
	req, err := p.toModelRequest()
	if err != nil {
		t.Fatalf("toModelRequest: unexpected error %v", err)
	}
	if req.TaskID != p.TaskID {
		t.Errorf("TaskID = %q, want %q", req.TaskID, p.TaskID)
	}
	if req.TaskClass != p.TaskClass {
		t.Errorf("TaskClass = %q, want %q", req.TaskClass, p.TaskClass)
	}
	if req.Sensitivity != provider.SensitivityInternal {
		t.Errorf("Sensitivity = %v, want SensitivityInternal", req.Sensitivity)
	}
	if req.FanOut != p.FanOut {
		t.Errorf("FanOut = %d, want %d", req.FanOut, p.FanOut)
	}
	if req.Requirements != p.Requirements {
		t.Errorf("Requirements = %+v, want %+v", req.Requirements, p.Requirements)
	}
	if len(req.Inputs) != len(p.Inputs) {
		t.Fatalf("len(Inputs) = %d, want %d", len(req.Inputs), len(p.Inputs))
	}
	for i, m := range p.Inputs {
		if req.Inputs[i].Role != m.Role || req.Inputs[i].Content != m.Content {
			t.Errorf("Inputs[%d] = %+v, want role=%q content=%q", i, req.Inputs[i], m.Role, m.Content)
		}
	}
}

// TestConductorExecuteParams_ToModelRequest_UnknownSensitivityErrors
// proves toModelRequest propagates parseSensitivityTier's own error
// rather than papering over it with a default request.
func TestConductorExecuteParams_ToModelRequest_UnknownSensitivityErrors(t *testing.T) {
	p := conductorExecuteParams{TaskID: "t1", Sensitivity: "not-a-real-tier"}
	req, err := p.toModelRequest()
	if err == nil {
		t.Fatalf("toModelRequest = %+v, nil, want the unknown-sensitivity error", req)
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("toModelRequest error kind: got %v, want KindInvalidInput", err)
	}
}
