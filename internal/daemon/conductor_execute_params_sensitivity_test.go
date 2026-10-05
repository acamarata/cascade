package daemon

// Purpose (this file): row [1] of TestFormerParseSitesFailClosed for the
//   conductor.execute door (P1-BF-R126): the wire sensitivity parses only
//   through provider.ParseSensitivityTier, an unknown name is refused before
//   any ModelRequest exists, and an empty name is the restricted zero value.
// Inputs: conductorExecuteParams values built in-test.
// Outputs: assertions on toModelRequest's tier and refusal.
// Constraints: the refusal is checked by Kind AND message, never by
//   errors.Is alone (cascade sentinels compare Kind only).
// SPORT: internal/daemon conductor.execute sensitivity (CHANGE, P1-SEC-19).

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// sensitivityTiers lists every declared provider.SensitivityTier member in
// value order. It lives in test code only: production parses through
// provider.ParseSensitivityTier and keeps no member list of its own.
var sensitivityTiers = []provider.SensitivityTier{
	provider.SensitivityRestricted,
	provider.SensitivityLocalOnly,
	provider.SensitivityInternal,
	provider.SensitivityPublic,
}

// TestFormerParseSitesFailClosed is this package's row of the former-parse-
// site table: conductor.execute's wire sensitivity. An unknown name (any
// case or whitespace variant included) refuses with KindInvalidInput naming
// the value and builds no request; the empty name builds a request at the
// restricted zero value. Restoring the old local name table with a
// permissive fallback turns the unknown cases red.
func TestFormerParseSitesFailClosed(t *testing.T) {
	t.Run("conductor_execute_params", func(t *testing.T) {
		for _, bad := range []string{"RESTRICTED ", "Restricted", "secret", "normal", "local_only"} {
			req, err := conductorExecuteParams{TaskID: "t", Sensitivity: bad}.toModelRequest()
			if err == nil {
				t.Fatalf("sensitivity %q built a request at %v; want a refusal", bad, req.Sensitivity)
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("sensitivity %q: kind = %v, want KindInvalidInput", bad, err)
			}
			if !strings.Contains(err.Error(), "conductor.execute: sensitivity") || !strings.Contains(err.Error(), `"`+bad+`"`) {
				t.Fatalf("sensitivity %q: error %q must name the door and the value", bad, err)
			}
			if req.TaskID != "" {
				t.Fatalf("sensitivity %q: a refused call still returned a populated request %+v", bad, req)
			}
		}
		req, err := conductorExecuteParams{TaskID: "t", Sensitivity: ""}.toModelRequest()
		if err != nil {
			t.Fatalf("empty sensitivity: unexpected refusal %v", err)
		}
		if req.Sensitivity != provider.SensitivityRestricted {
			t.Fatalf("empty sensitivity = %v, want the restricted zero value", req.Sensitivity)
		}
		for _, want := range sensitivityTiers {
			req, err := conductorExecuteParams{TaskID: "t", Sensitivity: want.String()}.toModelRequest()
			if err != nil || req.Sensitivity != want {
				t.Fatalf("sensitivity %q = %v, %v; want %v, nil", want.String(), req.Sensitivity, err, want)
			}
		}
	})
}
