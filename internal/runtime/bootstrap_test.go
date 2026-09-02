package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrap_ComposesPathsProfileAndConfig(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".cascade")
	rt, err := Bootstrap(context.Background(), BootstrapOptions{
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return home, nil },
		Environ: fakeEnviron(nil),
		Clock:   NewFixedClock(time.Unix(0, 0)),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if rt.Profile != DefaultProfile {
		t.Errorf("Profile = %q, want default %q", rt.Profile, DefaultProfile)
	}
	if rt.Paths.Root() != root {
		t.Errorf("Paths.Root() = %q, want %q", rt.Paths.Root(), root)
	}
	if rt.Config.Runtime.Home != root {
		t.Errorf("Config.Runtime.Home = %q, want %q", rt.Config.Runtime.Home, root)
	}
	if rt.Config.Runtime.DataDir != rt.Paths.DataDir() {
		t.Errorf("Config.Runtime.DataDir = %q, want %q", rt.Config.Runtime.DataDir, rt.Paths.DataDir())
	}
}

func TestBootstrap_ProfileFlagWins(t *testing.T) {
	home := t.TempDir()
	rt, err := Bootstrap(context.Background(), BootstrapOptions{
		ProfileFlag: "worker",
		Getenv: func(k string) string {
			if k == "CASCADE_PROFILE" {
				return "server"
			}
			return ""
		},
		HomeDir: func() (string, error) { return home, nil },
		Environ: fakeEnviron(nil),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if rt.Profile != ProfileWorker {
		t.Errorf("Profile = %q, want worker", rt.Profile)
	}
}

func TestBootstrap_InvalidProfileFlagFails(t *testing.T) {
	home := t.TempDir()
	_, err := Bootstrap(context.Background(), BootstrapOptions{
		ProfileFlag: "not-a-profile",
		Getenv:      func(string) string { return "" },
		HomeDir:     func() (string, error) { return home, nil },
		Environ:     fakeEnviron(nil),
	})
	if err == nil {
		t.Fatal("expected an error for an invalid --profile flag, got nil")
	}
}

func TestBootstrap_DefaultClockWhenUnset(t *testing.T) {
	home := t.TempDir()
	rt, err := Bootstrap(context.Background(), BootstrapOptions{
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return home, nil },
		Environ: fakeEnviron(nil),
	})
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if rt.Clock == nil {
		t.Fatal("Clock = nil, want a default SystemClock")
	}
	if rt.Clock.Now().IsZero() {
		t.Error("default Clock.Now() returned the zero time")
	}
}

func TestFixedClock_AdvanceIsDeterministic(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewFixedClock(start)
	if !c.Now().Equal(start) {
		t.Fatalf("Now() = %v, want %v", c.Now(), start)
	}
	next := c.Advance(time.Hour)
	want := start.Add(time.Hour)
	if !next.Equal(want) || !c.Now().Equal(want) {
		t.Errorf("Advance = %v, Now() = %v, want %v", next, c.Now(), want)
	}
}

func TestSystemClock_ReportsNonZero(t *testing.T) {
	c := NewSystemClock()
	if c.Now().IsZero() {
		t.Error("SystemClock.Now() returned the zero time")
	}
}
