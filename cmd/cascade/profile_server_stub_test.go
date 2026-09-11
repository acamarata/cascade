//go:build !postgres

// Purpose: unit coverage for assembleServerProfile's !postgres refusal
//   path: asserts a nil profile and a real, named KindUnsupported error
//   -- never a fabricated success -- since providers/postgres and
//   providers/pgvector are not linked into this binary.
// SPORT: cmd/cascade (ADD, coverage-floor fix).

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAssembleServerProfile_UnsupportedBuild_RealRefusal(t *testing.T) {
	env := func(string) (string, bool) { return "", false }
	profile, err := assembleServerProfile(context.Background(), env, runtime.NewSystemClock())
	if profile != nil {
		t.Errorf("assembleServerProfile profile = %+v, want nil", profile)
	}
	if err == nil {
		t.Fatal("assembleServerProfile error = nil, want the -tags=postgres refusal")
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("error kind: got %v, want KindUnsupported", err)
	}
	if !strings.Contains(err.Error(), "-tags=postgres") {
		t.Errorf("error = %q, want it to name the missing build tag", err.Error())
	}
}
