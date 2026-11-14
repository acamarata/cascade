package main

import (
	"encoding/json"
	"testing"
)

// TestFleetLearnReset_NoChangeRendering proves a reset that matched no row
// renders as an explicit no-op, while a deleting reset keeps its count.
func TestFleetLearnReset_NoChangeRendering(t *testing.T) {
	if got := (learnResetResult{}).String(); got != "no change (0 observations matched)" {
		t.Errorf("empty reset render = %q", got)
	}
	if got := (learnResetResult{RowsDeleted: 1, Changed: true}).String(); got != "deleted 1 rows changed=true" {
		t.Errorf("deleting reset render = %q", got)
	}
}

// TestFleetLearnReset_ParamsShape proves --all alone sends no filter
// fields, and a scoped reset sends only the filters given.
func TestFleetLearnReset_ParamsShape(t *testing.T) {
	cases := []struct {
		in   learnResetParams
		want string
	}{
		{learnResetParams{All: true}, `{"all":true}`},
		{learnResetParams{ScopeKey: "global", Tier: "T1"}, `{"scope_key":"global","tier":"T1","all":false}`},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != tc.want {
			t.Errorf("reset params = %s, want %s", raw, tc.want)
		}
	}
}

// TestFleetLearnReset_FlagsDeclared proves the four reset flags exist.
func TestFleetLearnReset_FlagsDeclared(t *testing.T) {
	cmd := newFleetLearnResetCmd(fleetSessionsDeps{})
	for _, name := range []string{"all", "scope", "task-class", "tier"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("fleet learn reset has no --%s flag", name)
		}
	}
}
