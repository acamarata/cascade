// Purpose: InstallMiddleware publishes exactly one plugin.registered per
// successful plugin.add or plugin.update, after the commit, from the stored
// record, and nothing on a refusal or for any other method.
// SPORT: internal/plugins install middleware tests.
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// registeredEvents replays every plugin lifecycle event on bus.
func registeredEvents(t *testing.T, bus *events.Bus) []events.Event {
	t.Helper()
	evs, err := bus.Replay(context.Background(), PluginEventNamespace, 0)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return evs
}

func TestInstallMiddlewarePublishesAfterCommitOnly(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Unix(4000, 0)))
	t.Cleanup(func() { _ = bus.Close() })
	mw := InstallMiddleware(bus, store)
	raw := json.RawMessage(`{"id":"demo","checksum":"c0ffee","manifest_bytes":"c2VjcmV0"}`)

	var eventsAtCommit int
	commit := func(context.Context, json.RawMessage) (any, error) {
		eventsAtCommit = len(registeredEvents(t, bus))
		return "ok", SaveMetadata(ctx, store, PluginMetadata{Name: "demo", InstalledVersion: "1.2.0", Enabled: true,
			RuntimeMode: plugin.RuntimeWasm, Grants: []string{"net.http"}, PinnedChecksum: "c0ffee"})
	}
	refuse := func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("refused") }

	if _, err := mw("plugin.add", refuse)(ctx, raw); err == nil {
		t.Fatal("the refusal was swallowed")
	}
	if n := len(registeredEvents(t, bus)); n != 0 {
		t.Fatalf("%d events after a refusal, want 0", n)
	}
	for i, method := range []string{"plugin.add", UpdateRPCMethod} {
		if res, err := mw(method, commit)(ctx, raw); err != nil || res != "ok" {
			t.Fatalf("%s: %v, %v", method, res, err)
		}
		if eventsAtCommit != i {
			t.Fatalf("%s: %d events existed while the handler ran, want %d (publish must follow next)", method, eventsAtCommit, i)
		}
	}
	evs := registeredEvents(t, bus)
	if len(evs) != 2 {
		t.Fatalf("%d events for two commits, want 2", len(evs))
	}
	for _, ev := range evs {
		if ev.Kind != events.EventKindPluginRegistered || string(ev.Payload) != `{"id":"demo","version":"1.2.0","runtime":"wasm"}` {
			t.Fatalf("event = %s %s, want plugin.registered with id, version and runtime only", ev.Kind, ev.Payload)
		}
	}
}

func TestInstallMiddlewarePassesOtherMethodsAndMissingRecords(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Unix(4000, 0)))
	t.Cleanup(func() { _ = bus.Close() })
	ok := rpc.HandlerFunc(func(context.Context, json.RawMessage) (any, error) { return "ok", nil })
	mw := InstallMiddleware(bus, store)
	seedInstalled(t, store, "1.0.0", nil)
	for _, call := range []struct {
		method string
		raw    string
	}{
		{"plugin.remove", `{"id":"demo"}`},   // not an install verb
		{"plugin.add", `{"id":"absent"}`},    // no stored record
		{"plugin.add", `{"name":"demo"}`},    // no id
		{UpdateRPCMethod, `not json at all`}, // undecodable
	} {
		if _, err := mw(call.method, ok)(ctx, json.RawMessage(call.raw)); err != nil {
			t.Fatalf("%s %s: %v", call.method, call.raw, err)
		}
	}
	if n := len(registeredEvents(t, bus)); n != 0 {
		t.Fatalf("%d events, want 0", n)
	}
	for _, nilMW := range []rpc.MiddlewareFunc{InstallMiddleware(nil, store), InstallMiddleware(bus, nil)} {
		if res, err := nilMW("plugin.add", ok)(ctx, json.RawMessage(`{"id":"demo"}`)); err != nil || res != "ok" {
			t.Fatalf("nil-dependency middleware changed the call: %v %v", res, err)
		}
	}
	if n := len(registeredEvents(t, bus)); n != 0 {
		t.Fatalf("%d events from the nil-dependency middleware, want 0", n)
	}
}

// TestInstallMiddlewareLogsAFailedPublish: a commit whose event cannot be
// published still succeeds (it happened), and the failure is logged.
func TestInstallMiddlewareLogsAFailedPublish(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)
	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Unix(4000, 0)))
	if err := bus.Close(); err != nil { // every Publish now fails
		t.Fatal(err)
	}
	var logged bytes.Buffer
	mw := InstallMiddleware(bus, store, slog.New(slog.NewTextHandler(&logged, nil)))
	ok := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	res, err := mw(UpdateRPCMethod, ok)(ctx, json.RawMessage(`{"id":"demo"}`))
	if err != nil || res != "ok" {
		t.Fatalf("a committed call whose publish failed returned %v, %v; want its result", res, err)
	}
	out := logged.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "plugin.registered event not published") ||
		!strings.Contains(out, "method=plugin.update") || !strings.Contains(out, "after Close") {
		t.Fatalf("log = %q, want one warning naming the method and the publish error", out)
	}
}

// TestInstallMiddlewareRefusesRemoteBeforeHandler: the elevated install
// verbs refuse a remote-runtime manifest before their handler runs, with or
// without a bus to publish to.
func TestInstallMiddlewareRefusesRemoteBeforeHandler(t *testing.T) {
	store := storetest.NewMemStore()
	bus := events.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Unix(4000, 0)))
	t.Cleanup(func() { _ = bus.Close() })
	raw, _ := json.Marshal(UpdateParams{ID: "demo", ManifestBytes: []byte(remoteManifest)})
	for _, mw := range []rpc.MiddlewareFunc{InstallMiddleware(bus, store), InstallMiddleware(nil, nil)} {
		for _, method := range []string{"plugin.add", UpdateRPCMethod} {
			ran := 0
			_, err := mw(method, func(context.Context, json.RawMessage) (any, error) { ran++; return "ok", nil })(context.Background(), raw)
			requireRefusal(t, err, cascade.KindUnsupported, "DEF-P2-remote-plugin-runtime")
			if ran != 0 {
				t.Fatalf("%s: the handler ran %d times for a remote manifest", method, ran)
			}
		}
	}
	if n := len(registeredEvents(t, bus)); n != 0 {
		t.Fatalf("%d events for refused remote installs, want 0", n)
	}
}
