//go:build integration && !windows

// Purpose: prove `cascade fleet learn status|explain|reset` reach the real
// learn handlers on a live daemon socket, over a database seeded through the
// scorer's own write path before the daemon starts.
// Inputs: a fresh home, seeded observations, the composed RPC server.
// Outputs: the CLI's --json and human output for each verb.
// Constraints: no real HOME, vault or network beyond the throwaway unix socket.
// SPORT: cmd/cascade daemon learn wiring verification.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// startSeededLearnDaemon seeds three observations into a fresh daemon
// database, then serves the composed server on the daemon socket. It returns
// the deps the CLI dials with.
func startSeededLearnDaemon(t *testing.T) fleetSessionsDeps {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	runDeps := newRunTestDeps(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	_, paths, settings, err := loadDaemonConfig(ctx, runDeps)
	if err != nil {
		t.Fatalf("loadDaemonConfig: %v", err)
	}
	store, rawDB, closeStore, err := openRuntimeStore(ctx, paths, runDeps.Clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	if err := learn.ApplyLearnSchema(ctx, rawDB, migrate.SQLiteEmitter{}, runDeps.Clock); err != nil {
		t.Fatalf("apply learn schema: %v", err)
	}
	scorer := learn.NewSQLiteCapabilityScorer(rawDB, runDeps.Clock)
	for _, success := range []bool{true, true, false} {
		if err := scorer.Observe(ctx, "global", conductor.TaskClassCode, capacity.TierOne, success); err != nil {
			t.Fatalf("seed observation: %v", err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, manifest, connections, err := buildRPCServer(events.New(store, runDeps.Clock), runDeps.Clock, logger, settings, paths, nil, store,
		withDaemonRuntime(nil, rawDB, nil, nil, daemonDeps{}))
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, daemon.RunOptions{
			Settings: settings, PIDPath: daemon.PIDFilePath(paths), Logger: logger, Clock: runDeps.Clock,
			Server: server, Environ: runDeps.Environ, Manifest: manifest, Connections: connections,
		})
	}()
	t.Cleanup(func() { cancel(); <-done; closeStore() })
	waitForSocket(t, paths.SocketPath())
	return fleetSessionsDeps{Paths: paths, DialContext: client.UnixDialer}
}

// runLearnCLI drives `cascade fleet learn <args...>` against the daemon and
// returns its combined output.
func runLearnCLI(t *testing.T, deps fleetSessionsDeps, args ...string) string {
	t.Helper()
	root := &cobra.Command{Use: "cascade"}
	flags := root.PersistentFlags()
	for _, f := range []string{"json", "quiet", "verbose", "no-color"} {
		flags.Bool(f, false, "")
	}
	root.AddCommand(newFleetLearnCmd(deps))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"learn"}, args...))
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("fleet learn %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

// decodeLearnJSON runs a --json verb and decodes the envelope's data into out.
func decodeLearnJSON(t *testing.T, deps fleetSessionsDeps, out any, args ...string) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(runLearnCLI(t, deps, args...)), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		t.Fatalf("decode data %s: %v", env.Data, err)
	}
}

// TestFleetLearnCLIOverDaemonSocket reaches the real status, explain and
// reset handlers through the CLI over the daemon socket.
func TestFleetLearnCLIOverDaemonSocket(t *testing.T) {
	deps := startSeededLearnDaemon(t)

	var status learnStatusResult
	decodeLearnJSON(t, deps, &status, "status", "--json")
	var seeded int
	for _, s := range status.Scores {
		if s.Tier == string(capacity.TierOne) && s.TaskClass == string(conductor.TaskClassCode) && s.Observations == 3 {
			seeded++
		}
	}
	if seeded != 1 || len(status.Scores) != 1 {
		t.Fatalf("status = %+v, want exactly the seeded row", status.Scores)
	}

	var explain learnExplainResult
	decodeLearnJSON(t, deps, &explain, "explain", "global", string(conductor.TaskClassCode), "--json")
	if len(explain.Scores) == 0 || explain.Tier0 == nil || explain.BestLower == nil || explain.Threshold <= 0 {
		t.Fatalf("explain = %+v, want scores, both estimates and a threshold", explain)
	}

	var first, second learnResetResult
	decodeLearnJSON(t, deps, &first, "reset", "--all", "--json")
	if first.RowsDeleted != 1 || !first.Changed {
		t.Fatalf("first reset = %+v, want 1 row deleted", first)
	}
	decodeLearnJSON(t, deps, &second, "reset", "--all", "--json")
	if second.RowsDeleted != 0 || second.Changed {
		t.Fatalf("second reset = %+v, want no change", second)
	}
	if out := runLearnCLI(t, deps, "reset", "--all"); !strings.Contains(out, "no change (0 observations matched)") {
		t.Fatalf("human reset output = %q", out)
	}
}
