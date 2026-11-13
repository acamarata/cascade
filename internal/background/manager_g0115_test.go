package background

import (
	"testing"
	"time"
)

func TestRerunAfterExit(t *testing.T) {
	cases := []struct {
		name     string
		pass     Pass
		emptyRun int
		want     Action
	}{
		{
			name:     "exited empty pass run 1",
			pass:     Pass{Exited: true, Empty: true},
			emptyRun: 1,
			want:     ActionRestart,
		},
		{
			name:     "exited empty pass run 2",
			pass:     Pass{Exited: true, Empty: true},
			emptyRun: 2,
			want:     ActionRestart,
		},
		{
			name:     "active short pass",
			pass:     Pass{Duration: 1 * time.Second, Exited: false, Empty: true},
			emptyRun: 1,
			want:     ActionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.pass, tc.emptyRun)
			if got != tc.want {
				t.Errorf("Decide(%v, %d) = %q, want %q", tc.pass, tc.emptyRun, got, tc.want)
			}
		})
	}
}

func TestNegativeNoRerunOnNonEmpty(t *testing.T) {
	cases := []struct {
		name     string
		pass     Pass
		emptyRun int
		want     Action
	}{
		{
			name:     "completed non-empty pass count 0",
			pass:     Pass{Duration: 5 * time.Second, Empty: false, Exited: true},
			emptyRun: 0,
			want:     ActionNone,
		},
		{
			name:     "completed non-empty pass stale count 3",
			pass:     Pass{Duration: 5 * time.Second, Empty: false, Exited: true},
			emptyRun: 3,
			want:     ActionNone,
		},
		{
			name:     "negative duration",
			pass:     Pass{Duration: -1, Empty: false, Exited: true},
			emptyRun: 1,
			want:     ActionNone,
		},
		{
			name:     "negative emptyRun",
			pass:     Pass{Duration: 0, Empty: true, Exited: true},
			emptyRun: -1,
			want:     ActionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.pass, tc.emptyRun)
			if got != tc.want {
				t.Errorf("Decide(%v, %d) = %q, want %q", tc.pass, tc.emptyRun, got, tc.want)
			}
		})
	}
}

func TestKillStalledPass(t *testing.T) {
	cases := []struct {
		name     string
		pass     Pass
		emptyRun int
		want     Action
	}{
		{
			name:     "active pass duration 26 minutes",
			pass:     Pass{Duration: 26 * time.Minute, Exited: false, Empty: true},
			emptyRun: 1,
			want:     ActionKill,
		},
		{
			name:     "active pass duration exactly 25 minutes",
			pass:     Pass{Duration: 25 * time.Minute, Exited: false, Empty: true},
			emptyRun: 1,
			want:     ActionNone,
		},
		{
			name:     "completed non-empty pass duration 26 minutes",
			pass:     Pass{Duration: 26 * time.Minute, Exited: true, Empty: false},
			emptyRun: 1,
			want:     ActionNone,
		},
		{
			name:     "negative duration with active pass",
			pass:     Pass{Duration: -1 * time.Minute, Exited: false, Empty: true},
			emptyRun: 1,
			want:     ActionNone,
		},
		{
			name:     "negative emptyRun with active pass",
			pass:     Pass{Duration: 26 * time.Minute, Exited: false, Empty: true},
			emptyRun: -1,
			want:     ActionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.pass, tc.emptyRun)
			if got != tc.want {
				t.Errorf("Decide(%v, %d) = %q, want %q", tc.pass, tc.emptyRun, got, tc.want)
			}
		})
	}
}

func TestStopAfterThreeEmpties(t *testing.T) {
	cases := []struct {
		name     string
		pass     Pass
		emptyRun int
		want     Action
	}{
		{
			name:     "completed empty pass count 3",
			pass:     Pass{Duration: 5 * time.Second, Exited: true, Empty: true},
			emptyRun: 3,
			want:     ActionStop,
		},
		{
			name:     "completed empty pass count 4",
			pass:     Pass{Duration: 5 * time.Second, Exited: true, Empty: true},
			emptyRun: 4,
			want:     ActionStop,
		},
		{
			name:     "completed empty pass count 2",
			pass:     Pass{Duration: 5 * time.Second, Exited: true, Empty: true},
			emptyRun: 2,
			want:     ActionRestart,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.pass, tc.emptyRun)
			if got != tc.want {
				t.Errorf("Decide(%v, %d) = %q, want %q", tc.pass, tc.emptyRun, got, tc.want)
			}
		})
	}
}
