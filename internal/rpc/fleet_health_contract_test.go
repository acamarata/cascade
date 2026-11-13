package rpc

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The composition root is package main. Run its existing tests in that
// package so the RPC gate exercises the production wiring without copying it.
func runFleetHealthContract(t *testing.T, pkg, pattern string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "-v", "-count=2", "-shuffle=on", "-run", pattern, pkg)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("production contract: %v\n%s", err, raw)
	}
	if !strings.Contains(string(raw), "--- PASS: "+strings.Trim(pattern, "^$")+" (") {
		t.Fatalf("production contract did not run: %s", raw)
	}
}
func TestEXE14LegacyAC7(t *testing.T) {
	runFleetHealthContract(t, "../executive", "^TestEXE14LegacyAC7$")
}
func TestEXE14LegacyAC20(t *testing.T) {
	skipUnixDaemonContract(t)
	runFleetHealthContract(t, "../../cmd/cascade", "^TestEXE14LegacyAC20$")
}
func TestEXE14ProductionHandoff(t *testing.T) {
	if runtime.GOOS != "windows" {
		runFleetHealthContract(t, "../../cmd/cascade", "^TestEXE14ProductionHandoff$")
	}
	runFleetHealthContract(t, "../executive", "^TestHealthProductionHandoff$")
}

// skipUnixDaemonContract skips a contract whose cmd/cascade test lives in a
// !windows file: the daemon wiring it checks does not build on Windows.
func skipUnixDaemonContract(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the cmd/cascade daemon wiring is unix-only")
	}
}
