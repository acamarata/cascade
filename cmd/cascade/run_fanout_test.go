//go:build !windows && integration

// Purpose: P1-CORE-19 acceptance over the real socket and the CLI: the
//
//	durable fan-out delivers through a real daemon RPC server on a unix
//	socket, and `cascade run` mints, sends and checks the request id.
//
// SPORT: cmd/cascade/run (CHANGE, P1-CORE-19).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// serveRig serves r's registry on a real unix socket and returns a client.
func serveRig(t *testing.T, r *fanRig) (*http.Client, string) {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := daemon.NewRPCServer(r.reg, nil)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}, sock
}

func TestConductorExecuteFanOutDurable(t *testing.T) {
	r := newFanRig(t, rigOpts{})
	client, _ := serveRig(t, r)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "method": daemon.ConductorExecuteMethod,
		"params": fanParams(3, "", "durable fan-out prompt")})
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		Result struct {
			RequestID string            `json:"request_id"`
			Legs      []json.RawMessage `json:"legs"`
		} `json:"result"`
		Error *rpc.ErrorObject `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil || env.Error != nil {
		t.Fatalf("response: err=%v rpc=%+v", err, env.Error)
	}
	if got := r.prov.calls.Load(); got != 3 || len(env.Result.Legs) != 3 || env.Result.RequestID == "" {
		t.Fatalf("provider calls %d, legs %d, request_id %q; want 3, 3, set", got, len(env.Result.Legs), env.Result.RequestID)
	}
	es := r.entries(t, env.Result.RequestID)
	if kindCount(es, journal.KindResumeCursor) != 1 || kindCount(es, journal.KindFanOutLegStarted) != 3 || kindCount(es, journal.KindFanOutLegDone) != 3 {
		t.Fatalf("journal = %d entries, want one cursor, 3 started, 3 done", len(es))
	}
	for _, e := range es {
		if bytes.Contains(e.Payload, []byte("How can I help")) || bytes.Contains(e.Payload, []byte("durable fan-out prompt")) {
			t.Fatalf("journal entry %d carries request or response content: %s", e.Seq, e.Payload)
		}
	}
	if legs, reqs := r.counts(t); legs != 0 || reqs != 0 {
		t.Fatalf("after delivery: %d leg results, %d request records; want 0, 0 (DeleteTask)", legs, reqs)
	}
}

// stubDaemon answers every conductor.execute with result, counting calls.
func stubDaemon(t *testing.T, result string) (string, *atomic.Int32) {
	t.Helper()
	sock, calls := filepath.Join(shortTempDir(t), "s.sock"), new(atomic.Int32)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + result + `}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock, calls
}

// runCLI runs `cascade run args...` dialing sock; stderr is returned.
func runCLI(t *testing.T, sock string, args ...string) (string, error) {
	t.Helper()
	deps := runDeps{Paths: fakeDaemonPaths{root: t.TempDir()}, Getenv: func(string) string { return "" },
		Environ: func() []string { return nil },
		DialContext: func(ctx context.Context, _ string) (net.Conn, error) {
			if sock == "" {
				t.Fatal("the CLI dialed the daemon; want a client-side refusal")
			}
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}
	cmd, stderr := newRunCmd(deps), &bytes.Buffer{}
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(stderr)
	cmd.SetIn(bytes.NewReader(nil))
	err := cmd.ExecuteContext(context.Background())
	return stderr.String(), err
}

func TestConductorExecuteFanOutBounds(t *testing.T) {
	r, ctx := newFanRig(t, rigOpts{}), context.Background()
	valid := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for name, p := range map[string]map[string]any{
		"fan_out 17": fanParams(17, "", "p"), "fan_out 10000": fanParams(10000, "", "p"),
		"id 25": fanParams(2, valid[:25], "p"), "id 27": fanParams(2, valid+"A", "p"),
		"id I": fanParams(2, "I"+valid[1:], "p"), "id L": fanParams(2, "L"+valid[1:], "p"),
		"id O": fanParams(2, "O"+valid[1:], "p"), "id U": fanParams(2, "U"+valid[1:], "p"),
	} {
		if _, kind, msg := r.call(ctx, t, p); kind != "invalid-input" {
			t.Fatalf("%s: kind %q (%s), want invalid-input", name, kind, msg)
		}
	}
	js := journal.New(r.store, r.clock, journal.DefaultNamespace)
	if ents, _ := js.ListEntities(ctx); len(ents) != 0 || r.prov.calls.Load() != 0 {
		t.Fatalf("entities %v, provider calls %d; want zero writes and zero calls", ents, r.prov.calls.Load())
	}
	if legs, reqs := r.counts(t); legs+reqs != 0 {
		t.Fatalf("records %d/%d, want none", legs, reqs)
	}
	if _, err := runCLI(t, "", "--task", "chat", "--fan-out", "17", "--input", "hi"); err == nil {
		t.Fatal("cascade run --fan-out 17: want a client-side refusal")
	}
	if _, err := runCLI(t, "", "--task", "chat", "--resume", valid, "--input", "hi"); err == nil {
		t.Fatal("cascade run --resume without --fan-out >= 2: want a client-side refusal")
	}
}

func TestConductorExecuteFanOutEchoesRequestID(t *testing.T) {
	r := newFanRig(t, rigOpts{})
	_, sock := serveRig(t, r)
	stderr, err := runCLI(t, sock, "--task", "chat", "--fan-out", "2", "--sensitivity", "public", "--input", "hi")
	if err != nil || !strings.Contains(stderr, "cascade: request id ") || r.prov.calls.Load() != 2 {
		t.Fatalf("real daemon: err=%v stderr=%q calls=%d; want delivery and the printed request id", err, stderr, r.prov.calls.Load())
	}
	stub, calls := stubDaemon(t, `{"job_id":"j1","legs":[]}`)
	_, err = runCLI(t, stub, "--task", "chat", "--fan-out", "2", "--input", "hi")
	if err == nil || !strings.Contains(err.Error(), "daemon does not support re-attach") || calls.Load() != 1 {
		t.Fatalf("stub without echo: err=%v calls=%d; want the re-attach refusal after exactly one call", err, calls.Load())
	}
}

func TestConductorExecuteFanOutIDCollisionRefuses(t *testing.T) {
	fixed := func() (cascade.ID, error) { return cascade.ID("01ARZ3NDEKTSV4RRFFQ69G5FAV"), nil }
	r, ctx := newFanRig(t, rigOpts{edit: func(f *daemon.ConductorFanOut) { f.IDSource = fixed }}), context.Background()
	if _, kind, msg := r.call(ctx, t, fanParams(3, "", "first")); kind != "" {
		t.Fatalf("first call: %s %s", kind, msg)
	}
	head := len(r.entries(t, "01ARZ3NDEKTSV4RRFFQ69G5FAV"))
	_, kind, _ := r.call(ctx, t, fanParams(3, "", "second"))
	legs, reqs := r.counts(t)
	if kind != "internal" || r.prov.calls.Load() != 3 || legs+reqs != 0 || len(r.entries(t, "01ARZ3NDEKTSV4RRFFQ69G5FAV")) != head {
		t.Fatalf("collision: kind %q calls %d records %d; want internal, 3, 0 and no new entries", kind, r.prov.calls.Load(), legs+reqs)
	}
}

// denyPolicy refuses every request at the executor's policy door.
type denyPolicy struct{}

func (denyPolicy) Authorize(context.Context, provider.ModelRequest) error {
	return cascade.New(cascade.KindPolicyDenied, "test: policy denies this request")
}

func TestConductorExecuteFanOutPolicyRefusal(t *testing.T) {
	r, ctx := newFanRig(t, rigOpts{policy: denyPolicy{}}), context.Background()
	_, kind, _ := r.call(ctx, t, fanParams(3, "", "secret prompt"))
	js := journal.New(r.store, r.clock, journal.DefaultNamespace)
	ents, err := js.ListEntities(ctx)
	legs, reqs := r.counts(t)
	if kind != "policy-denied" || err != nil || len(ents) != 0 || legs+reqs != 0 || r.prov.calls.Load() != 0 {
		t.Fatalf("kind %q, entities %v, records %d, calls %d; want policy-denied and nothing persisted", kind, ents, legs+reqs, r.prov.calls.Load())
	}
}

func TestConductorExecuteFanOutReattachForeignEntity(t *testing.T) {
	r, ctx := newFanRig(t, rigOpts{}), context.Background()
	js := journal.New(r.store, r.clock, journal.DefaultNamespace)
	x, y := "01BX5ZZKBKACTAV9WEVGEMMVRZ", "01BX5ZZKBKACTAV9WEVGEMMVRY"
	if _, err := js.Append(ctx, x, journal.KindIntent, "dag-op", json.RawMessage(`{"action_id":"dag"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := js.Append(ctx, "fanout:"+y, journal.KindResumeCursor, "fence", json.RawMessage(`{"t":"fence","action_id":"y"}`)); err != nil {
		t.Fatal(err)
	}
	if res, kind, msg := r.call(ctx, t, fanParams(2, x, "new path")); kind != "" || res["request_id"] != x {
		t.Fatalf("request_id X: %s %s; want the NEW path to deliver", kind, msg)
	}
	if h, _ := js.HeadSeq(ctx, x); h != 1 {
		t.Fatalf("DAG entity X head = %d, want 1 (never touched)", h)
	}
	calls := r.prov.calls.Load()
	_, kind, _ := r.call(ctx, t, fanParams(2, y, "foreign"))
	if h, _ := js.HeadSeq(ctx, "fanout:"+y); kind != "not-found" || h != 1 || r.prov.calls.Load() != calls {
		t.Fatalf("request_id Y: kind %q head %d calls %d; want not-found, head 1, no call", kind, h, r.prov.calls.Load()-calls)
	}
}

func TestConductorExecuteFanOutSameTaskIDIsolated(t *testing.T) {
	r, ctx := newFanRig(t, rigOpts{}), context.Background()
	ids := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			res, kind, msg := r.call(ctx, t, fanParams(3, "", "same task"))
			if kind != "" || len(res["legs"].([]any)) != 3 {
				t.Errorf("concurrent call: %s %s", kind, msg)
			}
			id, _ := res["request_id"].(string)
			ids <- id
		}()
	}
	a, b := <-ids, <-ids
	if a == "" || a == b || r.prov.calls.Load() != 6 {
		t.Fatalf("ids %q/%q calls %d; want disjoint fan-out ids and 6 calls", a, b, r.prov.calls.Load())
	}
	if len(r.entries(t, a)) == 0 || len(r.entries(t, b)) == 0 {
		t.Fatal("each call needs its own journal entity")
	}
	if _, kind, _ := r.call(ctx, t, fanParams(3, "", "same task")); kind != "" || r.prov.calls.Load() != 9 {
		t.Fatalf("third call: kind %q calls %d; want a fresh dispatch (9)", kind, r.prov.calls.Load())
	}
}

// probeStore runs probe before every Get, then reads the wrapped store.
type probeStore struct {
	provider.Store
	probe func()
}

func (s probeStore) Get(ctx context.Context, ns, key string) ([]byte, error) {
	s.probe()
	return s.Store.Get(ctx, ns, key)
}

// TestConductorRegistrationStoresHandlerFanOut runs the real "conductor"
// registration: the handler's claim is visible through w.ConductorFanOut's
// Claims during its journal read, and a claim taken through the field
// refuses the handler. HOME is a temp dir (file vault, no keychain).
func TestConductorRegistrationStoresHandlerFanOut(t *testing.T) {
	dir, clock, held, seen := t.TempDir(), runtime.NewSystemClock(), "01ARZ3NDEKTSV4RRFFQ69G5FAV", new(atomic.Bool)
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(fakeMemoryPaths{root: dir}.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	w := &daemonWiring{Ctx: context.Background(), Registry: rpc.NewRegistry(), Manifest: daemon.NewManifest(nil, clock),
		Clock: clock, Paths: fakeMemoryPaths{root: dir}}
	w.Store = probeStore{Store: storetest.NewMemStore(), probe: func() {
		if c := w.ConductorFanOut.Claims; c != nil && !c.TryClaim(held) {
			seen.Store(true)
		} else if c != nil {
			c.Release(held)
		}
	}}
	for _, reg := range daemonRegistrations {
		if reg.Name == "conductor" {
			if err := reg.Wire(w); err != nil || w.ConductorFanOut.Claims == nil {
				t.Fatalf("conductor registration: err=%v claims=%v", err, w.ConductorFanOut.Claims)
			}
		}
	}
	r := &fanRig{reg: w.Registry}
	if _, kind, msg := r.call(w.Ctx, t, fanParams(2, held, "probe")); !seen.Load() {
		t.Fatalf("handler call (%s %s): its claim was never visible through w.ConductorFanOut.Claims", kind, msg)
	}
	if other := "01BX5ZZKBKACTAV9WEVGEMMVRZ"; !w.ConductorFanOut.Claims.TryClaim(other) {
		t.Fatal("TryClaim(other) through the field = false, want true")
	} else if _, kind, msg := r.call(w.Ctx, t, fanParams(2, other, "probe")); kind != "conflict" || !strings.Contains(msg, "fan-out in progress") {
		t.Fatalf("claim held through the field: kind %q (%s), want conflict \"fan-out in progress\"", kind, msg)
	}
}
