package nodes

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// requireKindMsg asserts err carries kind and a message containing want.
func requireKindMsg(t *testing.T, err error, kind cascade.Kind, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %v containing %q", kind, want)
	}
	if k, ok := cascade.KindOf(err); !ok || k != kind {
		t.Fatalf("kind = %v (ok=%v), want %v; err=%v", k, ok, kind, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want message containing %q", err.Error(), want)
	}
}

// TestEnrollRecordsRoute: EnrollNode persists Route{User, Addr,
// HostKeyFP, SocketDir} from the payload and the verified fingerprint,
// and refuses a malformed socket_dir before anything is written.
func TestEnrollRecordsRoute(t *testing.T) {
	h := newTestHarness(t)
	h.host = "worker@127.0.0.1:2222"
	p := h.validPayload(t, TierWorkerTrusted)
	p.SocketDir = "/srv/cascade/nodes"

	if _, err := EnrollNode(context.Background(), h.deps, nil, p); err != nil {
		t.Fatalf("EnrollNode: %v", err)
	}
	rec, err := h.deps.Records.Get(p.NodeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := Route{User: "worker", Addr: "127.0.0.1:2222", HostKeyFP: h.hostFP, SocketDir: "/srv/cascade/nodes"}
	if rec.NodeRoute != want {
		t.Fatalf("NodeRoute = %+v, want %+v", rec.NodeRoute, want)
	}
	tgt, route, err := h.deps.Records.Target(p.NodeID)
	if err != nil || route != want || tgt != (Target{NodeID: p.NodeID, User: "worker", Addr: "127.0.0.1:2222"}) {
		t.Fatalf("Target = %+v, %+v, %v", tgt, route, err)
	}
	if tgt.knownHostsKey() != p.Host {
		t.Fatalf("knownHostsKey = %q, want the pinned host %q", tgt.knownHostsKey(), p.Host)
	}

	bad := map[string]string{
		"relative": "srv/cascade/nodes",
		"too long": "/" + strings.Repeat("a", maxSocketDirBytes),
		"nul byte": "/srv/cas\x00cade",
	}
	msgs := map[string]string{"relative": "not an absolute path", "too long": "exceeds 256 bytes", "nul byte": "NUL byte"}
	for name, dir := range bad {
		t.Run(name, func(t *testing.T) {
			h := newTestHarness(t)
			p := h.validPayload(t, TierWorkerTrusted)
			p.SocketDir = dir
			_, err := EnrollNode(context.Background(), h.deps, nil, p)
			requireKindMsg(t, err, cascade.KindInvalidInput, msgs[name])
			if _, gerr := h.deps.Records.Get(p.NodeID); gerr == nil {
				t.Fatal("a refused enrollment wrote a record")
			}
			raw := `{"node_id":"n","node_pubkey_b64":"k","host":"u@h","host_key_fingerprint":"f","node_signature_b64":"s","socket_dir":"` +
				strings.ReplaceAll(dir, "\x00", `\u0000`) + `"}`
			_, derr := DecodeEnrollHandshakePayload([]byte(raw))
			requireKindMsg(t, derr, cascade.KindInvalidInput, msgs[name])
		})
	}
}

// TestTargetRefusesIncompleteRoute: Target never guesses a route. Any
// missing route field is KindNotFound; a revoked or drained node is
// KindPermissionDenied.
func TestTargetRefusesIncompleteRoute(t *testing.T) {
	full := Route{User: "worker", Addr: "127.0.0.1:22", HostKeyFP: "aabb", SocketDir: "/srv/cascade/nodes"}
	cases := map[string]func(*Route){
		"no socket dir": func(r *Route) { r.SocketDir = "" },
		"no user":       func(r *Route) { r.User = "" },
		"no addr":       func(r *Route) { r.Addr = "" },
		"no host key":   func(r *Route) { r.HostKeyFP = "" },
		"no route":      func(r *Route) { *r = Route{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			store, id := enrolledRouteStore(t, full)
			rec, _ := store.Get(id)
			mutate(&rec.NodeRoute)
			if err := store.put(rec); err != nil {
				t.Fatal(err)
			}
			_, _, err := store.Target(id)
			requireKindMsg(t, err, cascade.KindNotFound, "has no complete enrolled route")
		})
	}

	store, _ := enrolledRouteStore(t, full)
	_, _, err := store.Target("unknown-node")
	requireKindMsg(t, err, cascade.KindNotFound, "no device record")

	store, id := enrolledRouteStore(t, full)
	if _, err := store.Drain(id); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Target(id)
	requireKindMsg(t, err, cascade.KindPermissionDenied, "is drained")

	store, id = enrolledRouteStore(t, full)
	if _, err := store.Revoke(id); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Target(id)
	requireKindMsg(t, err, cascade.KindPermissionDenied, "is revoked")
}

// enrolledRouteStore returns a store holding one enrolled node with route.
func enrolledRouteStore(t *testing.T, route Route) (*RecordStore, string) {
	t.Helper()
	store := NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Unix(0, 0)))
	id, _, err := GenerateIdentity(strings.NewReader(strings.Repeat("R", 64)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.enroll(id, TierWorkerTrusted, route); err != nil {
		t.Fatal(err)
	}
	return store, id.NodeID
}
