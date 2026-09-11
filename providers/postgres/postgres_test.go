//go:build postgres

// Purpose: unit tests for postgres.go's Open/Close/String that need no
//
//	live server — the empty-DSN refusal, and that a connection failure
//	never leaks the DSN's credentials into the returned error. The real
//	conformance run against a live server is integration_test.go's job
//	(docker-tagged, per Art.2).
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).
package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/postgres"
)

func TestOpen_EmptyDSN(t *testing.T) {
	_, err := postgres.Open(context.Background(), "")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(\"\") = %v, want KindInvalidInput", err)
	}
}

// TestOpen_ErrorNeverLeaksDSN dials an address nothing listens on (an
// unreachable server, one of the ticket's named error paths) with a DSN
// carrying a real-shaped password, and asserts the returned error's
// message contains neither the password nor the raw DSN string —
// A/S-01.T7's credential-custody rule extended to error messages.
func TestOpen_ErrorNeverLeaksDSN(t *testing.T) {
	const secret = "s3cr3t-p4ssw0rd"
	dsn := "postgres://cascade_user:" + secret + "@127.0.0.1:1/nosuchdb?sslmode=disable&connect_timeout=1"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := postgres.Open(ctx, dsn)
	if err == nil {
		t.Fatal("Open against an unreachable address returned nil error, want a connection failure")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("Open error leaked the DSN password: %v", err)
	}
	if strings.Contains(msg, dsn) {
		t.Fatalf("Open error leaked the raw DSN: %v", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) && !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("Open(unreachable) = %v, want KindUnavailable or KindPermissionDenied", err)
	}
}
