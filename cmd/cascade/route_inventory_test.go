//go:build !windows

// Purpose: the route inventory over every local-socket HTTP surface the
//   binary serves: the composed daemon server (composeDaemon), the
//   `cascade mcp serve --socket` server (serveSocketReal) and the node
//   serve server (runNodeServe). Each one gets, on POST /rpc, GET /events,
//   every key of the daemon's SSEMux Topics and an unknown topic, a
//   non-owner peer and three browser-shaped requests, and must answer the
//   guard's fixed 403 body before any topic handler runs.
// Inputs: a fresh HOME and CASCADE_HOME per server; the real peer is this
//   test process, made a non-owner with rpc.WithOwnerUIDForTest.
// Outputs: test failures naming the server, route and case.
// Constraints: requests travel over each server's real unix socket, dialed
//   with the first-party client.UnixDialer and written as raw HTTP/1.1
//   (route_inventory_socket_test.go), so
//   the server's own ConnContext resolves a real peer UID; this file
//   imports neither "net" nor "net/http" (the no-network unit-test gate).
//   Topic handlers are swapped for recorders before the first request, and
//   owner-shaped control requests prove every probe reaches them.
// SPORT: cmd/cascade route inventory (TestRouteInventoryRefusesNonOwnerAndBrowser) [ADD].

package main

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// The guard's two fixed refusal bodies (internal/rpc/request_guard.go,
// written through http.Error, which appends a newline).
const (
	ownerRefusal   = "forbidden: socket peer is not the daemon owner\n"
	browserRefusal = "forbidden: browser-shaped request refused\n"
)

// probeCase is one refused request shape: header overrides on top of a
// first-party request, the body the guard must answer with, and whether it
// only applies to POST (the guard checks Content-Type on POST alone).
type probeCase struct {
	name     string
	header   map[string]string
	want     string
	postOnly bool
}

var browserCases = []probeCase{
	{"origin", map[string]string{"Origin": "http://evil"}, browserRefusal, false},
	{"empty-origin", map[string]string{"Origin": ""}, browserRefusal, false},
	{"sec-fetch-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, browserRefusal, false},
	{"sec-fetch-mode", map[string]string{"Sec-Fetch-Mode": "no-cors"}, browserRefusal, false},
	{"host-localhost", map[string]string{"Host": "localhost"}, browserRefusal, false},
	{"form-post", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, browserRefusal, true},
}

var nonOwnerCase = probeCase{"non-owner-peer", nil, ownerRefusal, false}

// unroutedTargets are requests no local server routes: past POST /rpc and
// GET /events (with its topics on the daemon) everything is a 404, so a new
// route mounted at an allowed composition point fails here.
var unroutedTargets = []string{"GET /", "GET /rpc", "OPTIONS /rpc", "POST /rpc/", "POST /events",
	"GET /healthz", "GET /metrics", "GET /debug/pprof/", "POST /admin"}

// roundTrip sends one request and returns its status and, when the
// response has a Content-Length, its body.
type roundTrip func(t *testing.T, method, target string, header map[string]string) (int, string)

// callCounter is a recording topic handler's invocation count.
type callCounter interface{ Calls() int32 }

// recordingTopic counts ServeHTTP calls. It is generic over the handler
// signature so this file names no net/http type; recorderFor instantiates
// it from an existing handler method value.
type recordingTopic[W, R any] struct{ calls atomic.Int32 }

func (h *recordingTopic[W, R]) ServeHTTP(W, R) { h.calls.Add(1) }
func (h *recordingTopic[W, R]) Calls() int32   { return h.calls.Load() }

// ConsumesEventVisible lets the access layer admit an owner control request
// to an owner-view topic, as it admits the real handler the recorder
// replaces; the guard under test runs before that check either way.
func (h *recordingTopic[W, R]) ConsumesEventVisible() {}

func recorderFor[W, R any](func(W, R)) *recordingTopic[W, R] { return new(recordingTopic[W, R]) }

// TestRouteInventoryRefusesNonOwnerAndBrowser is acceptance [0].
func TestRouteInventoryRefusesNonOwnerAndBrowser(t *testing.T) {
	t.Run("daemon", func(t *testing.T) {
		sock, routes, recs := serveComposedDaemon(t)
		assertRefusals(t, socketTrip(sock), routes, recs)
		assertTopicControls(t, socketTrip(sock), routes, recs)
		assertUnrouted(t, socketTrip(sock))
	})
	t.Run("mcp", func(t *testing.T) {
		sock := serveMCPSocket(t)
		assertRefusals(t, socketTrip(sock), []string{"POST /rpc"}, nil)
		assertNoEventsRoute(t, socketTrip(sock))
	})
	t.Run("node-serve", func(t *testing.T) {
		sock := serveNodeSocket(t)
		assertRefusals(t, socketTrip(sock), []string{"POST /rpc"}, nil)
		assertNoEventsRoute(t, socketTrip(sock))
	})
	t.Run("seeded-unguarded-mux", func(t *testing.T) {
		rec := recorderFor(rpc.NewSSEMux(nil).ServeHTTP)
		trip := func(_ *testing.T, method, target string, header map[string]string) (int, string) {
			req, w := httptest.NewRequest(method, target, nil), httptest.NewRecorder()
			for k, v := range header {
				req.Header.Set(k, v)
			}
			rec.ServeHTTP(w, req)
			return w.Code, w.Body.String()
		}
		routes := []string{"GET /events", "GET /events?topic=fleet.sessions"}
		fails := refusalFailures(t, trip, routes, browserCases, []callCounter{rec})
		if len(fails) == 0 {
			t.Fatal("an SSE handler served with no guardLocalRequest passed the inventory; the check cannot fail")
		}
	})
}

// assertRefusals runs the browser cases as the owner, then the non-owner case
// with the owner UID moved off this process, and fails on any request that
// was not refused with 403 and the fixed body, or that reached a recorder.
func assertRefusals(t *testing.T, trip roundTrip, routes []string, recs []callCounter) {
	t.Helper()
	for _, f := range refusalFailures(t, trip, routes, browserCases, recs) {
		t.Error(f)
	}
	t.Run(nonOwnerCase.name, func(t *testing.T) {
		rpc.WithOwnerUIDForTest(t, os.Getuid()+1)
		for _, f := range refusalFailures(t, trip, routes, []probeCase{nonOwnerCase}, recs) {
			t.Error(f)
		}
	})
}

// refusalFailures returns one line per request that was not a 403 with the
// case's body, or after which any recorder had run.
func refusalFailures(t *testing.T, trip roundTrip, routes []string, cases []probeCase, recs []callCounter) []string {
	t.Helper()
	var out []string
	for _, route := range routes {
		method, target, _ := strings.Cut(route, " ")
		for _, c := range cases {
			if c.postOnly && method != "POST" {
				continue
			}
			status, body := trip(t, method, target, c.header)
			if status != 403 || body != c.want {
				out = append(out, fmt.Sprintf("%s [%s]: got %d %q, want 403 %q", route, c.name, status, body, c.want))
			}
			for i, r := range recs {
				if n := r.Calls(); n != 0 {
					out = append(out, fmt.Sprintf("%s [%s]: recorder %d ran %d time(s)", route, c.name, i, n))
				}
			}
		}
	}
	return out
}

// serveComposedDaemon composes the real daemon, swaps every SSEMux topic
// and the default stream for recorders, serves the composed *http.Server
// on an owner socket, and returns the socket, the inventory routes (POST
// /rpc, GET /events, each topic, an unknown topic) and the recorders, the
// default stream's last.
func serveComposedDaemon(t *testing.T) (string, []string, []callCounter) {
	t.Helper()
	c := composeForTest(t)
	mux := c.Events
	keys := make([]string, 0, len(mux.Topics))
	for k := range mux.Topics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 || mux.Topics["fleet.sessions"] == nil {
		t.Fatalf("composed SSEMux topics = %v, want fleet.sessions among them", keys)
	}
	routes := []string{"POST /rpc", "GET /events"}
	recs := make([]callCounter, 0, len(keys)+1)
	for _, k := range keys {
		rec := recorderFor(mux.ServeHTTP)
		mux.Topics[k], recs = rec, append(recs, rec)
		routes = append(routes, "GET /events?topic="+url.QueryEscape(k))
	}
	def := recorderFor(mux.ServeHTTP)
	mux.Default, recs = def, append(recs, def)
	routes = append(routes, "GET /events?topic=no.such.topic")

	sock := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := runtime.ListenOwnerSocket(sock)
	if err != nil {
		t.Fatalf("ListenOwnerSocket: %v", err)
	}
	srv := c.Opts.Server
	done := make(chan struct{})
	go func() { _ = srv.Serve(ln); close(done) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })
	return sock, routes, recs
}

// assertTopicControls proves the probes are not vacuous: as the owner with
// first-party headers, each topic reaches exactly its recorder, GET /events
// reaches the default stream, an unknown topic gets a JSON-RPC refusal past
// the guard (topic access, not a guard body) and POST /rpc dispatches.
func assertTopicControls(t *testing.T, trip roundTrip, routes []string, recs []callCounter) {
	t.Helper()
	topics := routes[2 : len(routes)-1]
	for i, route := range topics {
		_, target, _ := strings.Cut(route, " ")
		if status, _ := trip(t, "GET", target, nil); status != 200 || recs[i].Calls() != 1 {
			t.Errorf("owner %s: status %d, recorder calls %d; want 200 and 1", route, status, recs[i].Calls())
		}
	}
	if status, _ := trip(t, "GET", "/events", nil); status != 200 || recs[len(recs)-1].Calls() != 1 {
		t.Errorf("owner GET /events: status %d, default calls %d; want 200 and 1", status, recs[len(recs)-1].Calls())
	}
	status, body := trip(t, "GET", "/events?topic=no.such.topic", nil)
	if (status != 400 && status != 403) || !strings.HasPrefix(body, `{"jsonrpc"`) {
		t.Errorf("owner unknown topic: got %d %q, want a JSON-RPC topic refusal past the guard", status, body)
	}
	if status, body = trip(t, "POST", "/rpc", nil); status != 200 {
		t.Errorf("owner POST /rpc: status %d %q, want 200", status, body)
	}
}

// assertNoEventsRoute: the mcp and node servers mount no SSE stream, so
// GET /events (bare or with a topic) is not routed at all, the other
// unrouted targets are 404 too, and POST /rpc from the owner dispatches.
func assertNoEventsRoute(t *testing.T, trip roundTrip) {
	t.Helper()
	assertUnrouted(t, trip, "GET /events", "GET /events?topic=fleet.sessions")
	if status, body := trip(t, "POST", "/rpc", nil); status != 200 {
		t.Errorf("owner POST /rpc: status %d %q, want 200", status, body)
	}
}

// assertUnrouted requires a 404 for every unrouted target, as the owner.
func assertUnrouted(t *testing.T, trip roundTrip, extra ...string) {
	t.Helper()
	for _, route := range append(slices.Clone(unroutedTargets), extra...) {
		method, target, _ := strings.Cut(route, " ")
		if status, body := trip(t, method, target, nil); status != 404 {
			t.Errorf("owner %s: got %d %q, want 404 (no such route)", route, status, body)
		}
	}
}
