package nodes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestNewTunnelHeartbeatSender_DialErrorPropagates covers the sink's own
// dial-failure branch, distinct from heartbeat_test.go's happy-path
// round trip.
func TestNewTunnelHeartbeatSender_DialErrorPropagates(t *testing.T) {
	sender := NewTunnelHeartbeatSender(func(context.Context) (Conn, error) {
		return nil, errors.New("tunnel unreachable")
	})
	err := sender(context.Background(), HeartbeatFrame{NodeID: "n", EnrollmentID: "e", Sequence: 1})
	if err == nil {
		t.Fatal("expected the dial error to propagate")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

// writeErrConn is a Conn whose Write always fails, isolating the sink's
// req.Write error branch from its later ReadResponse/decode branches.
type writeErrConn struct{}

func (writeErrConn) Read([]byte) (int, error)  { return 0, errors.New("nothing to read") }
func (writeErrConn) Write([]byte) (int, error) { return 0, errors.New("write refused") }
func (writeErrConn) Close() error              { return nil }

func TestNewTunnelHeartbeatSender_WriteErrorPropagates(t *testing.T) {
	sender := NewTunnelHeartbeatSender(func(context.Context) (Conn, error) { return writeErrConn{}, nil })
	err := sender(context.Background(), HeartbeatFrame{NodeID: "n", EnrollmentID: "e", Sequence: 1})
	if err == nil {
		t.Fatal("expected the conn write error to propagate")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

func TestNewTunnelHeartbeatSender_ReadResponseErrorPropagates(t *testing.T) {
	conn := &bufConn{resp: bytes.NewReader([]byte("not an http response at all"))}
	sender := NewTunnelHeartbeatSender(func(context.Context) (Conn, error) { return conn, nil })
	err := sender(context.Background(), HeartbeatFrame{NodeID: "n", EnrollmentID: "e", Sequence: 1})
	if err == nil {
		t.Fatal("expected a malformed HTTP response to be refused")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

func TestNewTunnelHeartbeatSender_DecodeErrorPropagates(t *testing.T) {
	body := "not valid json"
	resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	conn := &bufConn{resp: bytes.NewReader([]byte(resp))}
	sender := NewTunnelHeartbeatSender(func(context.Context) (Conn, error) { return conn, nil })
	err := sender(context.Background(), HeartbeatFrame{NodeID: "n", EnrollmentID: "e", Sequence: 1})
	if err == nil {
		t.Fatal("expected a non-JSON response body to be refused")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

func TestNewTunnelHeartbeatSender_RejectedByController(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"unknown node"}}`
	resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	conn := &bufConn{resp: bytes.NewReader([]byte(resp))}
	sender := NewTunnelHeartbeatSender(func(context.Context) (Conn, error) { return conn, nil })
	err := sender(context.Background(), HeartbeatFrame{NodeID: "n", EnrollmentID: "e", Sequence: 1})
	if err == nil {
		t.Fatal("expected the controller's JSON-RPC error to be surfaced as a refusal")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("expected KindUnavailable, got %v (ok=%v)", k, ok)
	}
}

// TestRegisterHeartbeatHandler_DecodeErrorPropagates drives
// RegisterHeartbeatHandler's own decode-error branch through the real
// registry.Dispatch entry point (distinct from ProcessHeartbeat's own
// unit tests, which never go through the handler closure at all).
func TestRegisterHeartbeatHandler_DecodeErrorPropagates(t *testing.T) {
	registry := rpc.NewRegistry()
	deps, _, _, _ := newHeartbeatHarness(t)
	RegisterHeartbeatHandler(registry, deps)
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "node.heartbeat", Params: []byte(`{}`)})
	if errObj == nil {
		t.Fatal("expected a decode-error response for an empty/invalid frame")
	}
}

// TestProcessHeartbeat_PutErrorPropagates drives ProcessHeartbeat's own
// deps.Records.put error branch: the frame verifies fine, but the
// backend refuses the write that would advance LastSeen.
func TestProcessHeartbeat_PutErrorPropagates(t *testing.T) {
	backend := newMemRecordBackend()
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(backend, clock)
	id := testIdentity(t, "put-err")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := GenerateIdentity(fixedReader(t, "put-err"))
	if err != nil {
		t.Fatal(err)
	}
	deps := HeartbeatDeps{Records: store, Sequences: NewSequenceStore(), Clock: clock}
	frame := signedFrame(t, priv, id.NodeID, DeriveEnrollmentID(rec), 1)

	backend.saveErr = cascade.New(cascade.KindUnavailable, "simulated write failure")
	if _, err := ProcessHeartbeat(context.Background(), deps, frame); err == nil {
		t.Fatal("expected the backend save error to propagate")
	}
}
