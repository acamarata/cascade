package testkit

import (
	"testing"
	"time"
)

func TestScaleDeadline(t *testing.T) {
	const base = 10 * time.Second
	cases := []struct {
		name  string
		scale string
		load  float64
		cpus  int
		want  time.Duration
	}{
		{"default", "", 0, 8, base},
		{"explicit scale", "5", 0, 8, 5 * base},
		{"bad scale ignored", "fast", 0, 8, base},
		{"scale below one ignored", "0.1", 0, 8, base},
		{"idle load", "", 7.9, 8, base},
		{"load doubles", "", 16, 8, 2 * base},
		{"scale and load multiply", "2", 16, 8, 4 * base},
		{"capped", "5", 80, 8, 10 * base},
	}
	for _, c := range cases {
		if got := scaleDeadline(base, c.scale, c.load, c.cpus); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDeadlineHonoursEnvScale(t *testing.T) {
	t.Setenv(scaleEnv, "3")
	if got := Deadline(t, time.Second); got < 3*time.Second || got > 10*time.Second {
		t.Fatalf("Deadline = %s, want between 3s and the 10s cap", got)
	}
}
