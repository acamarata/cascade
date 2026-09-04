//go:build !windows && integration

// Purpose: the composition-root proof for `cascade recall`. It calls the
//
//	REAL buildRPCServer — the function platformDaemonRun calls — serves
//	the server it returns over a REAL unix socket, and drives the REAL
//	cobra command through the PRODUCTION SDK call seam against it.
//
//	What this catches that nothing else does: recall.query being
//	registered on a registry a test built, rather than on the one the
//	daemon builds. Deleting the registerRecallHandler call from
//	buildRPCServer turns this red with "method not found", which is
//	exactly the shape of the defect this repository has shipped six
//	times.
//
// Constraints: build-tagged "integration" because it imports "net"/
//
//	"net/http", which the no-network unit lane forbids (Art.7.2).
//
// SPORT: cmd.cascade.cmd.recall (ADD, per T-3 sport_updates).
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// startRecallDaemon builds the daemon's REAL RPC server and serves it over
// a real unix socket, returning recallDeps whose Call is the PRODUCTION
// one, so the test exercises the shipped transport rather than a stand-in.
func startRecallDaemon(t *testing.T) recallDeps {
	t.Helper()
	// Short directory: a unix socket path is capped near 104 bytes and
	// t.TempDir() embeds the test name. The daemon's own paths still live
	// under t.TempDir() (Art.7.1).
	sockDir, err := os.MkdirTemp("", "recallcli")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	paths := fakeMemoryPaths{root: t.TempDir()}
	clock := runtime.SystemClock{}
	bus := events.New(storetest.NewMemStore(), clock)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	settings := daemon.Settings{SocketPath: filepath.Join(sockDir, "daemon.sock")}

	server, _, _, err := buildRPCServer(bus, clock, logger, settings, paths, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	ln, err := net.Listen("unix", settings.SocketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server.ReadHeaderTimeout = 5 * time.Second
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	return recallDeps{
		Paths:   fakeMemoryPaths{root: sockDir},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Call:    clientRecallCall,
	}
}

// runRecallAgainstDaemon executes the real command against the live daemon.
func runRecallAgainstDaemon(t *testing.T, deps recallDeps, args ...string) (string, error) {
	t.Helper()
	cmd := newRecallCmd(deps)
	flags := cmd.Flags()
	flags.Bool("json", false, "")
	flags.Bool("quiet", false, "")
	flags.Bool("verbose", false, "")
	flags.Bool("no-color", false, "")
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// TestRecallIsReachableOnTheDaemonTheCompositionRootBuilds is the wiring
// proof. The daemon under test has no index built, so the correct answer
// is the catalog's own not-found refusal — which only a handler that RAN
// can produce. A daemon that never registered recall.query would answer
// method-not-found instead, and that is what this asserts against.
func TestRecallIsReachableOnTheDaemonTheCompositionRootBuilds(t *testing.T) {
	deps := startRecallDaemon(t)
	for _, method := range []string{recall.MethodQuery, recall.MethodSearchAlias} {
		t.Run(method, func(t *testing.T) {
			var result recall.QueryResult
			err := deps.Call(context.Background(), deps.Paths.SocketPath(), method,
				recall.QueryParams{Query: "anything", Scope: "project/cascade"}, &result)
			if err == nil {
				t.Fatal("an unbuilt index must refuse, not answer")
			}
			if strings.Contains(strings.ToLower(err.Error()), "method not found") {
				t.Fatalf("%s is not registered on the daemon's own registry: %v", method, err)
			}
			if !cascade.HasKind(err, cascade.KindNotFound) {
				t.Fatalf("err = %v, want the catalog's not-found refusal", err)
			}
		})
	}
}

// TestRecallCommandReachesTheRealDaemon runs the whole surface: the real
// cobra command, the real SDK client, the real socket, the real handler.
func TestRecallCommandReachesTheRealDaemon(t *testing.T) {
	deps := startRecallDaemon(t)
	out, err := runRecallAgainstDaemon(t, deps, "anything", "--scope", "project/cascade")
	if err == nil {
		t.Fatalf("an unbuilt index must refuse, not answer:\n%s", out)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want not-found", err)
	}
	if strings.Contains(err.Error(), t.TempDir()) || strings.Contains(err.Error(), "/Users") {
		t.Errorf("the diagnostic carried a machine path: %v", err)
	}
}

// TestRecallCommandRefusesAnEmptyQueryOverTheWire: the refusal is the
// daemon's, decoded by the SDK, and it keeps its Kind across the wire so
// the process exit code is the taxonomy's.
func TestRecallCommandRefusesAnEmptyQueryOverTheWire(t *testing.T) {
	deps := startRecallDaemon(t)
	_, err := runRecallAgainstDaemon(t, deps, "   ", "--scope", "project/cascade")
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want invalid-input", err)
	}
}
