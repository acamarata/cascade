package conductor

// Purpose (this file): the [conductor] restricted_external_lanes parser:
//   what it refuses (wildcards, non-strings, non-lists) and what it accepts.
//
// SPORT: conductor.restricted-permits (TEST) - P1-SEC-02.

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRestrictedPermitsParseRefusesWildcard(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   interface{}
	}{
		{"star", []interface{}{"*"}},
		{"star among names", []interface{}{"lane-a", "*"}},
		{"prefix glob", []interface{}{"lane-*"}},
		{"integer entry", []interface{}{"lane-a", 7}},
		{"bool entry", []interface{}{true}},
		{"nested list entry", []interface{}{[]interface{}{"lane-a"}}},
		{"empty entry", []interface{}{""}},
		{"padded entry", []interface{}{" lane-a"}},
		{"bare string value", "*"},
		{"map value", map[string]interface{}{"lane-a": true}},
		{"string slice with star", []string{"*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRestrictedPermits(map[string]interface{}{"restricted_external_lanes": tc.in})
			if err == nil {
				t.Fatal("ParseRestrictedPermits accepted a value that must refuse")
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("err = %v, want KindInvalidInput", err)
			}
			if !strings.Contains(err.Error(), "conductor.restricted_external_lanes") {
				t.Errorf("err %q does not name the key", err.Error())
			}
		})
	}
}

func TestRestrictedPermitsParseAccepts(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string]interface{}
		want []string
	}{
		{"nil section", nil, nil},
		{"absent key", map[string]interface{}{"spill_enabled": true}, nil},
		{"nil value", map[string]interface{}{"restricted_external_lanes": nil}, nil},
		{"empty list", map[string]interface{}{"restricted_external_lanes": []interface{}{}}, nil},
		{"two lanes", map[string]interface{}{"restricted_external_lanes": []interface{}{"lane-a", "lane-b"}}, []string{"lane-a", "lane-b"}},
		{"string slice", map[string]interface{}{"restricted_external_lanes": []string{"lane-a"}}, []string{"lane-a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseRestrictedPermits(tc.in)
			if err != nil {
				t.Fatalf("ParseRestrictedPermits: %v", err)
			}
			for _, l := range tc.want {
				if !p.Permits(l) {
					t.Errorf("lane %q not permitted", l)
				}
			}
			if p.Permits("lane-unlisted") {
				t.Error("an unlisted lane is permitted")
			}
			if len(tc.want) == 0 && (p.Permits("") || p.Permits("*")) {
				t.Error("the default list permits something")
			}
		})
	}
}
