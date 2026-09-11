// Purpose: unit tests for redis.go's Open/Close/String/error-classification
//   that need no real external server — miniredis (a pure-Go, real-RESP-
//   protocol in-memory server; github.com/alicebob/miniredis/v2, MIT) gives
//   these a genuine wire-compatible endpoint without docker, matching
//   providers/postgres_test.go's split: the full conformance run against a
//   REAL, docker-provisioned Redis is integration_test.go's job (Art.2).
// SPORT: providers.redis/ADDED (P1-E17-W4-S38-T6).

package redis_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/redis"
)

// startMiniredis starts a fresh miniredis server and returns its redis://
// URL, closing the server on test cleanup.
func startMiniredis(t *testing.T) string {
	t.Helper()
	srv, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(srv.Close)
	return "redis://" + srv.Addr()
}

func TestOpen_EmptyURL(t *testing.T) {
	_, err := redis.Open(context.Background(), "")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(\"\") = %v, want KindInvalidInput", err)
	}
}

func TestOpen_UnparseableURL(t *testing.T) {
	_, err := redis.Open(context.Background(), "not a url \x00")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(unparseable) = %v, want KindInvalidInput", err)
	}
}

func TestOpen_Success(t *testing.T) {
	url := startMiniredis(t)
	conn, err := redis.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if conn.String() == "" {
		t.Fatal("String() returned empty")
	}
}

// TestOpen_UnreachableServer proves the "unreachable server" error path
// against a real (non-listening) loopback address, and that the error
// never leaks the URL's credentials (08 §2 extended to error messages).
func TestOpen_UnreachableServer(t *testing.T) {
	const secret = "s3cr3t-p4ssw0rd"
	url := "redis://user:" + secret + "@127.0.0.1:1/0"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := redis.Open(ctx, url)
	if err == nil {
		t.Fatal("Open against an unreachable address returned nil error")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open(unreachable) = %v, want KindUnavailable", err)
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("Open error leaked the URL password: %v", err)
	}
}

// TestOpen_WrongPassword proves the auth-failure error path against a
// real (if embedded) server that genuinely requires and rejects a
// password — never simulated.
func TestOpen_WrongPassword(t *testing.T) {
	srv, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(srv.Close)
	srv.RequireAuth("correct-password")
	_, err = redis.Open(context.Background(), "redis://:wrong-password@"+srv.Addr())
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("Open(wrong password) = %v, want KindPermissionDenied", err)
	}
}

// TestOpen_ContextAlreadyCanceled proves the cancellation error path.
func TestOpen_ContextAlreadyCanceled(t *testing.T) {
	url := startMiniredis(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := redis.Open(ctx, url)
	if err == nil {
		t.Fatal("Open with an already-canceled context returned nil error")
	}
	if !cascade.HasKind(err, cascade.KindCanceled) && !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open(canceled ctx) = %v, want KindCanceled or KindUnavailable", err)
	}
}

func TestClose_NilSafe(t *testing.T) {
	var conn *redis.Conn
	if err := conn.Close(); err != nil {
		t.Fatalf("nil Conn.Close() = %v, want nil", err)
	}
}
