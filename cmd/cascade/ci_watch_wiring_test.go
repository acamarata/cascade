//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// ciWatchDaemon is a real daemon on the production socket path whose GET
// /events mux is built by the production ci-watch-topic registration,
// plus a publisher replaying a scripted acceptance re-run.
type ciWatchDaemon struct {
	cancel context.CancelFunc
	done   chan error
	pub    chan error
}

// startCIWatchDaemon serves the ci topic on the socket the ci mount
// resolves and replays the re-run for jobID until stopped. Each replay
// uses a fresh checkpoint id, so a subscriber that joins mid-replay
// completes on the next one.
func startCIWatchDaemon(parent context.Context, jobID string, once bool) (*ciWatchDaemon, error) {
	settings, err := resolveStatusSocket(parent, productionStatusDeps())
	if err != nil {
		return nil, err
	}
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	mux, registry := rpc.NewSSEMux(nil), rpc.NewRegistry()
	if err := wireRegistration("ci-watch-topic", &daemonWiring{Events: mux, Registry: registry, Bus: bus, Clock: clock}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	d := &ciWatchDaemon{cancel: cancel, done: make(chan error, 1), pub: make(chan error, 1)}
	ready := make(chan struct{})
	go func() {
		defer func() { _ = bus.Close() }()
		d.done <- daemon.Run(ctx, daemon.RunOptions{
			Settings: settings, PIDPath: filepath.Join(filepath.Dir(settings.SocketPath), "ci-watch.pid"),
			Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Ready: ready, Signals: make(chan os.Signal), Server: daemon.NewRPCServer(registry, mux),
		})
	}()
	select {
	case <-ready:
	case err := <-d.done:
		cancel()
		return nil, fmt.Errorf("daemon did not start: %w", err)
	}
	if once {
		d.pub <- replayAcceptance(ctx, bus, jobID, true)
		return d, nil
	}
	go func() { d.pub <- replayAcceptance(ctx, bus, jobID, false) }()
	return d, nil
}

// stop ends the publisher and the daemon and reports either's failure.
func (d *ciWatchDaemon) stop() error {
	d.cancel()
	pubErr := <-d.pub
	if errors.Is(pubErr, context.Canceled) {
		pubErr = nil
	}
	return errors.Join(pubErr, <-d.done)
}

// wireRegistration runs the named production daemon registration.
func wireRegistration(name string, w *daemonWiring) error {
	for _, r := range daemonRegistrations {
		if r.Name == name {
			return r.Wire(w)
		}
	}
	return fmt.Errorf("no daemon registration %q", name)
}

// replayAcceptance publishes, every 50ms until ctx ends: a streaming
// checkpoint of jobID, an acceptance checkpoint of another job, then
// jobID's acceptance re-run (lint and unit dispatched, then terminal).
func replayAcceptance(ctx context.Context, bus *events.Bus, jobID string, once bool) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for n := 0; ; n++ {
		cp := fmt.Sprintf("cp-%d", n)
		steps := []struct {
			kind       events.EventKind
			job, rk    string
			acceptance bool
		}{
			{ci.EventKindCheckpointPlanned, jobID, "lint", false},
			{ci.EventKindCheckpointDispatched, jobID, "lint", false},
			{ci.EventKindCheckpointTerminal, jobID, "lint", false},
			{ci.EventKindCheckpointPlanned, "job-other", "lint", true},
			{ci.EventKindCheckpointDispatched, "job-other", "lint", true},
			{ci.EventKindCheckpointTerminal, "job-other", "lint", true},
			{ci.EventKindCheckpointPlanned, jobID, "lint,unit", true},
			{ci.EventKindCheckpointDispatched, jobID, "lint", true},
			{ci.EventKindCheckpointDispatched, jobID, "unit", true},
			{ci.EventKindCheckpointTerminal, jobID, "lint", true},
			{ci.EventKindCheckpointTerminal, jobID, "unit", true},
		}
		for i, s := range steps {
			id := cp
			switch {
			case s.job != jobID:
				id = "other-" + cp
			case !s.acceptance:
				id = "stream-" + cp
			}
			fields := map[string]any{"job_id": s.job, "checkpoint_id": id, "kind": s.rk,
				"acceptance": s.acceptance, "passed": true, "seq_in_replay": i}
			if s.kind == ci.EventKindCheckpointPlanned {
				fields["kind"], fields["kinds"] = "", strings.Split(s.rk, ",")
			}
			payload, _ := json.Marshal(fields)
			if _, err := bus.Publish(ctx, ci.EventNamespace, s.kind, "ci-watch-test", payload); err != nil {
				return err
			}
		}
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// ciWatchFixture is the running fixture of the ci_watch script.
var (
	ciWatchFixtureMu sync.Mutex
	ciWatchFixture   *ciWatchDaemon
)

// ciWatchFixtureEnv gates the fixture mount: the ci_watch script sets it
// (the driver's env is scoped to that script), so no other tree, golden or
// help output ever carries the fixture.
const ciWatchFixtureEnv = "CASCADE_TEST_CI_WATCH_FIXTURE"

// The script driver runs one synchronous command per line, so this hidden
// test-only mount starts the daemon (start <job-id>) and stops it (stop)
// around the production `cascade ci watch` the script runs in between.
var _ = registerRootMount(rootMount{Name: "ci-watch-fixture", Order: 990, Mount: func(root *cobra.Command) {
	if os.Getenv(ciWatchFixtureEnv) != "1" {
		return
	}
	root.AddCommand(&cobra.Command{Use: "ci-watch-fixture", Hidden: true, Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			ciWatchFixtureMu.Lock()
			defer ciWatchFixtureMu.Unlock()
			if args[0] == "start" && len(args) == 2 && ciWatchFixture == nil {
				d, err := startCIWatchDaemon(context.Background(), args[1], false)
				ciWatchFixture = d
				return err
			}
			if args[0] == "stop" && ciWatchFixture != nil {
				d := ciWatchFixture
				ciWatchFixture = nil
				return d.stop()
			}
			return fmt.Errorf("ci-watch-fixture: bad use %v", args)
		}})
}})

// shortHome points HOME, CASCADE_HOME and the socket at a fresh temp dir.
func shortHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade"))
	t.Setenv("CASCADE_SOCKET", filepath.Join(home, "s"))
}

func TestCIWatchBoundToClientStream(t *testing.T) {
	shortHome(t)
	d, err := startCIWatchDaemon(context.Background(), "job-7", false)
	if err != nil {
		t.Fatal(err)
	}
	got := execCascade([]string{"ci", "watch", "job-7"})
	if err := d.stop(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if got.exit != 0 {
		t.Fatalf("ci watch exit %d, want 0\nstdout: %s\nstderr: %s", got.exit, got.stdout, got.stderr)
	}
	for _, want := range []string{"ci.checkpoint.dispatched checkpoint=cp-", "kind=unit acceptance=true passed=true"} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, got.stdout)
		}
	}
	if strings.Contains(got.stdout, "other-cp") {
		t.Fatalf("another job's frame reached the watch:\n%s", got.stdout)
	}
}

func TestCIWatchTopicOwnerOnly(t *testing.T) {
	shortHome(t)
	d, err := startCIWatchDaemon(context.Background(), "job-7", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.stop() }()
	rpc.WithOwnerUIDForTest(t, os.Getuid()+1)
	if got := execCascade([]string{"ci", "watch", "job-7"}); got.exit != 7 {
		t.Fatalf("non-owner ci watch exit %d, want 7 (permission denied)\nstderr: %s", got.exit, got.stderr)
	}
}

// TestCIWatchAttachAfterRunEnded: the run finished before the watch
// started; the watch reads it from the event store and exits 0 at once.
func TestCIWatchAttachAfterRunEnded(t *testing.T) {
	shortHome(t)
	d, err := startCIWatchDaemon(context.Background(), "job-7", true)
	if err != nil {
		t.Fatal(err)
	}
	got := execCascade([]string{"ci", "watch", "job-7"})
	if err := d.stop(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if got.exit != 0 || !strings.Contains(got.stdout, "ci.checkpoint.planned checkpoint=cp-0 kinds=lint,unit acceptance=true") {
		t.Fatalf("ci watch after the run ended: exit %d, want 0 from the stored run\nstdout: %s\nstderr: %s", got.exit, got.stdout, got.stderr)
	}
}
