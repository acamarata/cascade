//go:build !windows

package main

// Regression: a fresh home started its daemon with no controller
// designation, so nodes.ResolveRole failed closed, the job scheduler never
// ran and `cascade doctor` reported the subsystem census as an error.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
)

func designationDeps(t *testing.T) daemonDeps {
	t.Helper()
	return daemonDeps{
		SecretsDir:  shortTempDir(t),
		NewKeystore: nodes.NewNodeKeystore,
		Clock:       runtime.NewFixedClock(time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)),
	}
}

func TestDaemonBootstrapsDesignationOnFreshHome(t *testing.T) {
	dataDir := t.TempDir()
	deps := designationDeps(t)
	binding := nodes.NewFileControllerBindingBackend(dataDir)
	if _, err := nodes.ResolveRole(binding); !errors.Is(err, nodes.ErrRoleConflict) {
		t.Fatalf("fresh home before start: want a role conflict, got %v", err)
	}
	bootstrapDaemonDesignation(context.Background(), deps, dataDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	role, err := nodes.ResolveRole(binding)
	if err != nil || role != nodes.RoleController {
		t.Fatalf("fresh home after start: role %q, err %v; want controller", role, err)
	}
	first, _, _ := nodes.NewFileDesignationBackend(dataDir).Load()
	if first.Epoch != 1 || first.Reason != "bootstrap" {
		t.Fatalf("designation %+v; want epoch 1, reason bootstrap", first)
	}
	again, wrote, err := ensureDaemonDesignation(context.Background(), deps, dataDir)
	if err != nil || wrote || again != first {
		t.Fatalf("second start rewrote the designation: %+v wrote=%v err=%v", again, wrote, err)
	}
}

func TestDaemonDoesNotBootstrapAfterNodeBind(t *testing.T) {
	dataDir := t.TempDir()
	tomb := filepath.Join(dataDir, "nodes", "bound_once")
	if err := os.MkdirAll(filepath.Dir(tomb), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tomb, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, wrote, err := ensureDaemonDesignation(context.Background(), designationDeps(t), dataDir)
	if wrote || !errors.Is(err, nodes.ErrRoleConflict) {
		t.Fatalf("tombstoned home: wrote=%v err=%v; want a role conflict and no write", wrote, err)
	}
	if _, found, _ := nodes.NewFileDesignationBackend(dataDir).Load(); found {
		t.Fatal("tombstoned home gained a designation")
	}
}

func TestDaemonDesignationNeedsDataDir(t *testing.T) {
	if _, wrote, err := ensureDaemonDesignation(context.Background(), designationDeps(t), ""); err == nil || wrote {
		t.Fatalf("empty data dir: wrote=%v err=%v; want an error", wrote, err)
	}
}
