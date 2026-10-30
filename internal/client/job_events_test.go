//go:build !windows

package client_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ciTopicDaemon runs a real daemon whose GET /events serves the production
// ci topic handler on a real unix socket, and returns a client for it.
func ciTopicDaemon(t *testing.T) (*client.Client, *events.Bus) {
	t.Helper()
	return ciDaemon(t, true)
}

// ciDaemon is ciTopicDaemon, optionally without the ci topic mounted.
func ciDaemon(t *testing.T, withTopic bool) (*client.Client, *events.Bus) {
	t.Helper()
	// Not t.TempDir(): on hosted macOS runners its path plus the test name
	// pushes d.sock past the 104-byte sun_path limit.
	home, err := os.MkdirTemp("", "je")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	registry := rpc.NewRegistry()
	registry.Register(ci.MethodWatchReplayEnd, ci.WatchReplayEndHandler(bus))
	mux := rpc.NewSSEMux(nil)
	if withTopic {
		mux.WithTopic(ci.WatchTopic, ci.NewWatchSSEHandler(bus, clock))
	}
	sock := filepath.Join(home, "d.sock")
	ctx, cancel := context.WithCancel(context.Background())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx, daemon.RunOptions{
			Settings: daemon.Settings{SocketPath: sock}, PIDPath: filepath.Join(home, "pid"),
			Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Ready: ready, Signals: make(chan os.Signal), Server: daemon.NewRPCServer(registry, mux),
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("daemon: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not start")
	}
	return client.New(sock, client.UnixDialer, time.Second), bus
}

func publishCI(t *testing.T, bus *events.Bus, kind events.EventKind, job, kindName string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"job_id": job, "checkpoint_id": "cp-1", "kind": kindName, "acceptance": true})
	if _, err := bus.Publish(context.Background(), ci.EventNamespace, kind, "test", payload); err != nil {
		t.Fatal(err)
	}
}

func TestJobEventsStreamsJobFrames(t *testing.T) {
	c, bus := ciTopicDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frames, closeStream, err := c.JobEvents(ctx, "job-1")
	if err != nil {
		t.Fatalf("JobEvents: %v", err)
	}
	defer closeStream()
	// Another job's frame and another kind on the namespace are not job-1's.
	publishCI(t, bus, ci.EventKindCheckpointDispatched, "job-2", "lint")
	publishCI(t, bus, ci.EventKindRunCompleted, "job-1", "lint")
	publishCI(t, bus, ci.EventKindCheckpointDispatched, "job-1", "lint")
	publishCI(t, bus, ci.EventKindCheckpointTerminal, "job-1", "lint")
	if f := <-frames; f.Event != "ci.watch.replayed" || f.ID != "" {
		t.Fatalf("first frame on an empty store = %+v, want the replay marker", f)
	}
	var got []client.JobEventFrame
	for len(got) < 2 {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("stream closed after %v", got)
			}
			got = append(got, f)
		case <-ctx.Done():
			t.Fatalf("frames: %v", got)
		}
	}
	if got[0].Event != string(ci.EventKindCheckpointDispatched) || got[1].Event != string(ci.EventKindCheckpointTerminal) || got[0].ID == "" {
		t.Fatalf("frames = %+v, want job-1's dispatched then terminal with resume ids", got)
	}
	var p struct {
		JobID string `json:"job_id"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(got[1].Data), &p); err != nil || p.JobID != "job-1" || p.Kind != "lint" {
		t.Fatalf("Data = %q (%v), want the job-1 lint payload", got[1].Data, err)
	}
	closeStream()
	select {
	case _, ok := <-frames:
		if ok {
			t.Fatal("frame after close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closer did not end the stream")
	}
}

func TestJobEventsRefusesNonOwner(t *testing.T) {
	c, _ := ciTopicDaemon(t)
	rpc.WithOwnerUIDForTest(t, os.Getuid()+1)
	_, closeStream, err := c.JobEvents(context.Background(), "job-1")
	defer closeStream()
	if kind, _ := cascade.KindOf(err); kind != cascade.KindPermissionDenied {
		t.Fatalf("non-owner subscriber: %v, want KindPermissionDenied", err)
	}
	if _, _, err := c.JobEvents(context.Background(), ""); err == nil {
		t.Fatal("an empty job id was accepted")
	}
}

func TestJobEventsFailsClosed(t *testing.T) {
	if _, _, err := client.New(filepath.Join(t.TempDir(), "none.sock"), client.UnixDialer, time.Second).JobEvents(context.Background(), "job-1"); !isUnavailableHint(err) {
		t.Fatalf("no daemon: %v, want KindUnavailable naming `cascade daemon run`", err)
	}
	c, _ := ciDaemon(t, false)
	if _, _, err := c.JobEvents(context.Background(), "job-1"); !isUnavailableHint(err) {
		t.Fatalf("daemon without the ci topic: %v, want KindUnavailable naming `cascade daemon run`", err)
	}
	c, bus := ciTopicDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frames, closeStream, err := c.JobEvents(ctx, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream()
	if f := <-frames; f.Event != "ci.watch.replayed" {
		t.Fatalf("first frame = %+v, want the replay marker", f)
	}
	if _, err := bus.Publish(ctx, ci.EventNamespace, ci.EventKindCheckpointTerminal, "test", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	publishCI(t, bus, ci.EventKindCheckpointTerminal, "job-1", "lint")
	select {
	case f, ok := <-frames:
		if ok {
			t.Fatalf("an unreadable payload was skipped and %+v delivered; want the stream closed", f)
		}
	case <-ctx.Done():
		t.Fatal("an unreadable payload did not close the stream")
	}
}

func isUnavailableHint(err error) bool {
	kind, _ := cascade.KindOf(err)
	return kind == cascade.KindUnavailable && strings.Contains(err.Error(), "cascade daemon run")
}

// TestJobEventsReplaysRecordedRun: a run stored before the subscription is
// replayed from the event store, then the marker, then live events.
func TestJobEventsReplaysRecordedRun(t *testing.T) {
	c, bus := ciTopicDaemon(t)
	publishCI(t, bus, ci.EventKindCheckpointPlanned, "job-1", "")
	publishCI(t, bus, ci.EventKindCheckpointTerminal, "job-1", "lint")
	publishCI(t, bus, ci.EventKindCheckpointTerminal, "job-2", "lint")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frames, closeStream, err := c.JobEvents(ctx, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream()
	publishCI(t, bus, ci.EventKindCheckpointDispatched, "job-1", "unit")
	var got []string
	for len(got) < 4 {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("stream closed after %v", got)
			}
			got = append(got, f.Event)
		case <-ctx.Done():
			t.Fatalf("frames: %v", got)
		}
	}
	want := []string{string(ci.EventKindCheckpointPlanned), string(ci.EventKindCheckpointTerminal), "ci.watch.replayed", string(ci.EventKindCheckpointDispatched)}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("frames = %v, want the stored run, the marker, then the live frame: %v", got, want)
	}
}
