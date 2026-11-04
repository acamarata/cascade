package learn

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestValidateSupport_BeneficiaryLaneExcluded(t *testing.T) {
	end := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	beneficiary := LaneIdentity{"l1", "node-a"}
	row := func(id int64, lane LaneIdentity, at time.Time) OutcomeRow {
		return OutcomeRow{OutcomeID: id, JobID: "job-a", RepoID: "repo-a", Lane: lane, CompletedAt: at}
	}
	a := row(1, LaneIdentity{"l1", "node-b"}, end)
	b := row(2, LaneIdentity{"l2", "node-a"}, end.Add(-Window))
	c := row(3, LaneIdentity{"l2", "node-c"}, end.Add(-time.Hour))
	self := row(4, beneficiary, end.Add(Window*2))
	for _, tc := range []struct {
		name string
		rows []OutcomeRow
		want []OutcomeRow
	}{
		{"empty", nil, nil},
		{"only beneficiary", []OutcomeRow{self, self, self}, nil},
		{"beneficiary cannot supply third source", []OutcomeRow{a, b, self}, nil},
		{"two occurrences", []OutcomeRow{a, b}, nil},
		{"three occurrences one source", []OutcomeRow{a, a, a}, nil},
		{"three occurrences two sources", []OutcomeRow{a, a, b}, nil},
		{"exact thresholds and inclusive boundary", []OutcomeRow{a, b, c}, []OutcomeRow{a, b, c}},
		{"all beneficiary rows removed first", []OutcomeRow{self, a, self, b, c, self}, []OutcomeRow{a, b, c}},
		{"outside window", []OutcomeRow{a, row(2, b.Lane, end.Add(-Window-time.Nanosecond)), c}, nil},
		{"missing timestamp", []OutcomeRow{a, b, row(3, c.Lane, time.Time{})}, nil},
		{"missing lane node", []OutcomeRow{a, b, row(3, LaneIdentity{"l2", ""}, end)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]OutcomeRow(nil), tc.rows...)
			got, err := ValidateSupport(beneficiary, tc.rows)
			if tc.want == nil {
				if !errors.Is(err, ErrInsufficientSupport) || got != nil {
					t.Fatalf("got (%+v, %v), want insufficient support", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got (%+v, %v), want %+v", got, err, tc.want)
			}
			if !reflect.DeepEqual(tc.rows, before) {
				t.Fatal("input rows changed")
			}
		})
	}
}
