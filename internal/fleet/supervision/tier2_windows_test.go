//go:build windows

package supervision

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the Windows refusal, asserted rather than assumed.
//   06-FORGE-SPEC §2 scopes tier 2 to the platforms with a pseudo-terminal;
//   an operator here must be told plainly, and told what to do instead.
// SPORT: fleet.supervision tier-2 Windows tests (ADD) — P1-E18-W4-S39-T3.

// TestTier2RefusesOnWindows holds the three properties that make this a
// refusal rather than a stub: it returns an error of the right KIND, it
// returns no session, and it names the tiers that do work here.
func TestTier2RefusesOnWindows(t *testing.T) {
	ctx := context.Background()
	s, err := NewTier2Supervisor(NewPTYAttacher(), noEnv, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess, runErr := s.Run(ctx)
	if !errors.Is(runErr, ErrPTYUnavailable) {
		t.Fatalf("err = %v, want ErrPTYUnavailable", runErr)
	}
	if !cascade.HasKind(runErr, cascade.KindUnsupported) {
		t.Errorf("err kind = %v, want KindUnsupported", runErr)
	}
	if sess != nil {
		t.Error("a refused attach returned a session")
	}
	for _, want := range []string{"tier 1", "tier 3"} {
		if !strings.Contains(runErr.Error(), want) {
			t.Errorf("err = %q, want it to name %q as an alternative", runErr, want)
		}
	}
}
