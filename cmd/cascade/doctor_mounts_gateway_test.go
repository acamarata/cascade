package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
)

// TestDoctorReportsGatewayTermsSensitive exercises the mounted doctor report.
func TestDoctorReportsGatewayTermsSensitive(t *testing.T) {
	deps, _, _ := gatewayCLIDeps(t)
	if _, _, err := runProvider(t, deps, "add", "gw1", "--gateway", "acct"); err != nil {
		t.Fatal(err)
	}
	useTempCustody(t)
	doctorDeps := testDoctorDeps(t)
	doctorDeps.Registry = func(ctx context.Context) (*doctor.CheckRegistry, error) {
		return productionCheckRegistry(ctx, deps.Paths, doctorTestClock())
	}
	out, err := execRootDoctor(t, doctorDeps, "doctor")
	if err != nil && !strings.Contains(err.Error(), "outcome") {
		t.Fatal(err)
	}
	for _, want := range []string{"gateway", "gw1", "terms-sensitive", "operator-configured"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor missing %q", want)
		}
	}
}
