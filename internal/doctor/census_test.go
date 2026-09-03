package doctor

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeSubsystemProvider struct {
	declared []string
	running  map[string]bool
	declErr  error
	runErr   error
}

func (f fakeSubsystemProvider) DeclaredSubsystems(context.Context) ([]string, error) {
	return f.declared, f.declErr
}

func (f fakeSubsystemProvider) RunningSubsystems(context.Context) (map[string]bool, error) {
	return f.running, f.runErr
}

func TestDoctorSubsystemCensusCheck(t *testing.T) {
	t.Run("all declared running is ok", func(t *testing.T) {
		c := NewSubsystemCensusCheck(fakeSubsystemProvider{
			declared: []string{"storage", "events"},
			running:  map[string]bool{"storage": true, "events": true},
		})
		res, err := c.Run(context.Background())
		if err != nil || res.Status != StatusOK {
			t.Fatalf("got %+v, err=%v, want StatusOK", res, err)
		}
	})

	t.Run("declared but missing is error, never ok", func(t *testing.T) {
		c := NewSubsystemCensusCheck(fakeSubsystemProvider{
			declared: []string{"storage", "events", "scheduler"},
			running:  map[string]bool{"storage": true, "events": true},
		})
		res, _ := c.Run(context.Background())
		if res.Status != StatusError {
			t.Fatalf("got status=%v, want StatusError", res.Status)
		}
		if !strings.Contains(res.Detail, "scheduler") {
			t.Fatalf("detail %q does not name the missing subsystem", res.Detail)
		}
	})

	t.Run("absence in running map is never implied as running", func(t *testing.T) {
		c := NewSubsystemCensusCheck(fakeSubsystemProvider{
			declared: []string{"hooks"},
			running:  map[string]bool{}, // hooks absent entirely, not false
		})
		res, _ := c.Run(context.Background())
		if res.Status != StatusError {
			t.Fatalf("got status=%v, want StatusError (silent absence must not pass)", res.Status)
		}
	})
}

func TestDoctorSubsystemCensusCheck_ProviderFailures(t *testing.T) {
	t.Run("declared-manifest read failure is error, not ok", func(t *testing.T) {
		c := NewSubsystemCensusCheck(fakeSubsystemProvider{declErr: fmt.Errorf("manifest unavailable")})
		res, _ := c.Run(context.Background())
		if res.Status != StatusError {
			t.Fatalf("got status=%v, want StatusError", res.Status)
		}
	})

	t.Run("live-state read failure is error, not ok", func(t *testing.T) {
		c := NewSubsystemCensusCheck(fakeSubsystemProvider{declared: []string{"storage"}, runErr: fmt.Errorf("live state unavailable")})
		res, _ := c.Run(context.Background())
		if res.Status != StatusError {
			t.Fatalf("got status=%v, want StatusError", res.Status)
		}
	})
}

func TestDoctorSubsystemCensusCheck_FixAndMetadata(t *testing.T) {
	t.Run("not fixable", func(t *testing.T) {
		c := NewSubsystemCensusCheck(fakeSubsystemProvider{})
		if _, err := c.Fix(context.Background()); err != ErrCheckNotFixable {
			t.Fatalf("got err=%v, want ErrCheckNotFixable", err)
		}
	})

	t.Run("metadata is not first-run", func(t *testing.T) {
		c := NewSubsystemCensusCheck(fakeSubsystemProvider{})
		if c.Metadata().FirstRun {
			t.Fatalf("subsystem_census should not be tagged FirstRun")
		}
		if c.Name() != "subsystem_census" {
			t.Fatalf("got Name()=%q", c.Name())
		}
	})
}

func TestDoctorSubsystemCensusCheck_Describe(t *testing.T) {
	c := NewSubsystemCensusCheck(fakeSubsystemProvider{})
	if c.Describe() == "" {
		t.Fatalf("Describe() must not be empty")
	}
}
