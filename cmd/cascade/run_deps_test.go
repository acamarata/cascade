// Purpose: unit coverage for run.go's small untested surface:
//
//	productionRunDeps' field assembly, mountRunCmd's AddCommand wiring,
//	and validateSensitivity's three branches (empty, valid, invalid).
//
// SPORT: cmd.cascade.run/TEST (P1-E11-W3-S23-T1).
package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestProductionRunDeps_AssemblesRealEnvironment(t *testing.T) {
	deps := productionRunDeps()
	if deps.Paths == nil {
		t.Error("productionRunDeps: Paths is nil")
	}
	if deps.Getenv == nil {
		t.Error("productionRunDeps: Getenv is nil")
	}
	if deps.Environ == nil {
		t.Error("productionRunDeps: Environ is nil")
	}
	if deps.DialContext == nil {
		t.Error("productionRunDeps: DialContext is nil")
	}
}

func TestMountRunCmd_AddsRunCommand(t *testing.T) {
	root := &cobra.Command{Use: "cascade"}
	mountRunCmd(root)
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "run" {
			found = true
		}
	}
	if !found {
		t.Error("mountRunCmd did not add a \"run\" subcommand to root")
	}
}

func TestValidateSensitivity(t *testing.T) {
	if err := validateSensitivity(""); err != nil {
		t.Errorf("validateSensitivity(\"\") = %v, want nil (empty tier is always valid)", err)
	}
	if err := validateSensitivity("restricted"); err != nil {
		t.Errorf("validateSensitivity(\"restricted\") = %v, want nil", err)
	}
	if err := validateSensitivity("not-a-real-tier"); err == nil {
		t.Error("validateSensitivity(\"not-a-real-tier\") = nil, want a refusal")
	}
}
