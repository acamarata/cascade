package conductpolicy

import "testing"

// TestLaneValid verifies that Valid reports true only for the supported lanes.
func TestLaneValid(t *testing.T) {
	cases := []struct {
		name string
		lane Lane
		want bool
	}{
		{name: "bulk", lane: LaneBulk, want: true},
		{name: "premium", lane: LanePremium, want: true},
		{name: "empty", lane: Lane(""), want: false},
		{name: "wrong case", lane: Lane("Bulk"), want: false},
		{name: "trailing space", lane: Lane("bulk "), want: false},
		{name: "unknown", lane: Lane("other"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.lane.Valid(); got != tc.want {
				t.Errorf("Lane(%q).Valid() = %v, want %v", string(tc.lane), got, tc.want)
			}
		})
	}
}

// TestWorkKindValid verifies that Valid reports true only for the supported work kinds.
func TestWorkKindValid(t *testing.T) {
	cases := []struct {
		name string
		kind WorkKind
		want bool
	}{
		{name: "launch", kind: WorkLaunch, want: true},
		{name: "manage", kind: WorkManage, want: true},
		{name: "bulk", kind: WorkBulk, want: true},
		{name: "empty", kind: WorkKind(""), want: false},
		{name: "wrong case", kind: WorkKind("LAUNCH"), want: false},
		{name: "unknown", kind: WorkKind("run"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.kind.Valid(); got != tc.want {
				t.Errorf("WorkKind(%q).Valid() = %v, want %v", string(tc.kind), got, tc.want)
			}
		})
	}
}
