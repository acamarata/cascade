// Purpose: unit coverage for resolveRunSocket (run_exec.go), mirroring
//
//	status_test.go's TestResolveStatusSocket_* pair exactly for the
//	sibling function that shipped with no direct test of its own -- the
//	malformed-config refusal and the config-defaults-from-paths success
//	path.
//
// SPORT: cmd.cascade.run/TEST (P1-E11-W3-S23-T1).
package main

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestResolveRunSocket_MalformedConfig(t *testing.T) {
	dir := t.TempDir()
	deps := runDeps{
		Paths:   fakeDaemonPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte("this is not [ valid toml"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	_, err := resolveRunSocket(context.Background(), deps)
	if err == nil {
		t.Fatal("resolveRunSocket: expected an error for malformed config.toml")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

func TestResolveRunSocket_DefaultsFromPaths(t *testing.T) {
	dir := t.TempDir()
	deps := runDeps{
		Paths:   fakeDaemonPaths{root: dir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	settings, err := resolveRunSocket(context.Background(), deps)
	if err != nil {
		t.Fatalf("resolveRunSocket: unexpected error: %v", err)
	}
	if settings.SocketPath != deps.Paths.SocketPath() {
		t.Errorf("SocketPath = %q, want the path provider's default %q", settings.SocketPath, deps.Paths.SocketPath())
	}
}
