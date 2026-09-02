// Purpose: ParseSpec/NextAfter unit tests. R-14.117-authorized split of
//   scheduler_test.go.
// SPORT: internal.events.scheduler.ParseSpec/ADDED (tests)
//   (P1-E03-W1-S04-T4).

package scheduler

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseSpec_Every(t *testing.T) {
	sched, err := ParseSpec("@every 90m")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := sched.NextAfter(from)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	want := from.Add(90 * time.Minute)
	if !next.Equal(want) {
		t.Fatalf("NextAfter = %v, want %v", next, want)
	}
}

func TestParseSpec_EveryInvalid(t *testing.T) {
	cases := []string{"@every", "@every ", "@every 0h", "@every -1h", "@every not-a-duration"}
	for _, spec := range cases {
		if _, err := ParseSpec(spec); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("ParseSpec(%q) error = %v, want KindInvalidInput", spec, err)
		}
	}
}

func TestParseSpec_CronFields(t *testing.T) {
	// "0 3 * * *" = every day at 03:00.
	sched, err := ParseSpec("0 3 * * *")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	from := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) // noon Jan 1
	next, err := sched.NextAfter(from)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	want := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextAfter = %v, want %v", next, want)
	}
}

func TestParseSpec_CronFieldsWeekday(t *testing.T) {
	// "0 0 * * 0" = every Sunday at midnight.
	sched, err := ParseSpec("0 0 * * 0")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	// 2026-01-01 is a Thursday.
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := sched.NextAfter(from)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	if next.Weekday() != time.Sunday {
		t.Fatalf("NextAfter = %v, want a Sunday", next)
	}
	if !next.After(from) {
		t.Fatalf("NextAfter = %v, want strictly after %v", next, from)
	}
}

func TestParseSpec_StepAndList(t *testing.T) {
	sched, err := ParseSpec("*/15 * * * *")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	from := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	next, err := sched.NextAfter(from)
	if err != nil {
		t.Fatalf("NextAfter: %v", err)
	}
	want := time.Date(2026, 1, 1, 0, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextAfter = %v, want %v", next, want)
	}
}

func TestParseSpec_InvalidCronFields(t *testing.T) {
	cases := []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * * 13 *", "* * * * 7", "a b c d e",
		"1-2-3 * * * *", "*/0 * * * *", "5-1 * * * *",
	}
	for _, spec := range cases {
		if _, err := ParseSpec(spec); err == nil {
			t.Errorf("ParseSpec(%q) succeeded, want an error", spec)
		}
	}
}

func TestCronJob_Interval(t *testing.T) {
	j := CronJob{Spec: "@every 168h0m0s"}
	d, ok := j.Interval()
	if !ok || d != 168*time.Hour {
		t.Fatalf("Interval() = (%v, %v), want (168h, true)", d, ok)
	}

	j2 := CronJob{Spec: "0 0 * * 0"}
	if _, ok := j2.Interval(); ok {
		t.Fatalf("Interval() on a cron-field spec reported ok=true, want false")
	}

	j3 := CronJob{Spec: "garbage"}
	if _, ok := j3.Interval(); ok {
		t.Fatalf("Interval() on an unparseable spec reported ok=true, want false")
	}
}
