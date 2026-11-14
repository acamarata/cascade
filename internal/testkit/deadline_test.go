package testkit

import (
	"errors"
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

func TestParseLoadavg(t *testing.T) {
	if got := parseLoadavg([]byte("3.50 2.00 1.00 2/300 999\n"), nil); got != 3.5 {
		t.Fatalf("parse = %v, want 3.5", got)
	}
	if got := parseLoadavg([]byte("  \n"), nil); got != 0 {
		t.Fatalf("empty content = %v, want 0", got)
	}
	if got := parseLoadavg([]byte("busy"), nil); got != 0 {
		t.Fatalf("garbage = %v, want 0", got)
	}
	if got := parseLoadavg(nil, errors.New("no proc")); got != loadAverageSysctl() {
		t.Fatalf("read error = %v, want the sysctl fallback", got)
	}
	if loadAverage1() < 0 {
		t.Fatal("negative load")
	}
}
