//go:build postgres && integration

// Purpose: the socket legs of pgvector's credential hygiene. An unreachable
//
//	server (a closed loopback port) is KindUnavailable; a real pgvector
//	server refusing a wrong password is KindPermissionDenied; an in-memory
//	stub server (net.Pipe) answering 28P01 drives pgx's real auth-failure
//	path through Open and proves the refusal is never retried. No error
//	carries the password or the DSN. Canary passwords are built at run
//	time and never printed.
//
// Inputs: CASCADE_TEST_POSTGRES_DSN for the live refusal (the CI
//
//	pgvector-storetest-under-docker job's pgvector/pgvector:pg16 service);
//	the unreachable and stub cases need no server and never skip. The
//	unreachable case is also Open's pass-through leg: a parseable DSN and
//	a clean dial error keep the driver text as the cause.
package pgvector

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/acamarata/cascade/pkg/cascade"
)

// closedLoopbackPort returns a loopback TCP port nothing listens on: it
// binds an ephemeral port and closes the listener at once.
func closedLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

func TestPgvectorUnreachableIsUnavailable(t *testing.T) {
	isolatePGEnv(t)
	port := strconv.Itoa(closedLoopbackPort(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, dsn := range []string{
		"postgres://u:" + canary() + "@127.0.0.1:" + port + "/db?sslmode=disable&connect_timeout=3",
		"host=127.0.0.1 port=" + port + " user=u password=" + canary() + " dbname=db sslmode=disable connect_timeout=3",
	} {
		_, err := Open(ctx, dsn)
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Open(closed loopback port) = %s, want KindUnavailable", kindOf(err))
		}
		assertNoLeak(t, "unreachable", err, []string{canary(), dsn, "u:" + canary()})
		var cause *connCause // a parseable DSN and a clean refusal pass the driver text through
		if !errors.As(err, &cause) || cause.text == withheld || !strings.Contains(cause.text, "failed to connect") {
			t.Errorf("Open(closed loopback port): cause is not the clean driver text (found=%v)", cause != nil)
		}
	}
}

// wrongPasswordDSN returns dsn with its password replaced by pw. A URL
// DSN gets new userinfo and loses any query password; a key=value DSN
// gets a trailing password setting, which pgx applies last.
func wrongPasswordDSN(t *testing.T, dsn, pw string) string {
	t.Helper()
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return dsn + " password='" + pw + "'"
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		t.Fatalf("CASCADE_TEST_POSTGRES_DSN is not a URL with a user (parse ok=%v)", err == nil)
	}
	u.User = url.UserPassword(u.User.Username(), pw)
	q := u.Query()
	q.Del("password")
	u.RawQuery = q.Encode()
	return u.String()
}

func TestPgvectorLiveWrongPasswordRefused(t *testing.T) {
	base := os.Getenv("CASCADE_TEST_POSTGRES_DSN")
	if base == "" {
		t.Skip("CASCADE_TEST_POSTGRES_DSN not set: this case needs a real pgvector-enabled server (see providers/pgvector/testdata/README.md)")
	}
	isolatePGEnv(t)
	pw := strings.Join([]string{"wr0ng", "pw", canary()}, "-")
	dsn := wrongPasswordDSN(t, base, pw)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := Open(ctx, dsn)
	if err == nil {
		_ = d.Close()
		t.Fatal("Open(wrong password) succeeded, want KindPermissionDenied")
	}
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("Open(wrong password) kind = %s, want KindPermissionDenied", kindOf(err))
	}
	assertNoLeak(t, "live-wrong-password", err, []string{pw, url.QueryEscape(pw), dsn})
}

// stubDialer answers every dial with a net.Pipe whose far end reads the
// startup message and replies with a FATAL ErrorResponse carrying code.
// It never opens a socket; lookups resolve to a fixed address. The
// returned counter holds the number of dials.
func stubDialer(cfg *pgconn.Config, code string) *atomic.Int32 {
	dials := new(atomic.Int32)
	cfg.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		client, server := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			be := pgproto3.NewBackend(server, server)
			if _, err := be.ReceiveStartupMessage(); err != nil {
				return
			}
			be.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: "authentication failed"})
			_ = be.Flush()
		}()
		return client, nil
	}
	return dials
}

// TestPgvectorWrongPasswordIsNeverRetried is the fail-closed probe: a DSN
// naming three hosts whose first answers 28P01 is dialed once through
// Open, so no other host (and no other credential) is ever tried. It also
// runs Open's real pgx connect path to KindPermissionDenied without a leak.
func TestPgvectorWrongPasswordIsNeverRetried(t *testing.T) {
	isolatePGEnv(t)
	cfg, err := pgx.ParseConfig("host=h1,h2,h3 user=u password=" + canary() + " sslmode=disable")
	if err != nil {
		t.Fatalf("pgx.ParseConfig: %s", kindOf(err))
	}
	dials := stubDialer(&cfg.Config, "28P01")
	name := stdlib.RegisterConnConfig(cfg)
	defer stdlib.UnregisterConnConfig(name)
	_, err = Open(context.Background(), name)
	if n := dials.Load(); n != 1 {
		t.Fatalf("dials = %d, want exactly 1 (a refused password must not be retried)", n)
	}
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("Open(3 hosts, first answers 28P01) = %s, want KindPermissionDenied", kindOf(err))
	}
	assertNoLeak(t, "open-stub-28P01", err, []string{canary()})
}

// TestPgvectorAuthFailureNeverEchoesCredential sends every parseable
// adversarial DSN through pgx's real 28P01 path (the stub server) and
// wrapConnError: KindPermissionDenied, and nothing forbidden in the chain.
func TestPgvectorAuthFailureNeverEchoesCredential(t *testing.T) {
	isolatePGEnv(t)
	checked := 0
	for _, c := range advCases(canary()) {
		cfg, err := pgconn.ParseConfig(c.dsn)
		if err != nil {
			continue // unparseable DSNs never reach a dial; the unit file covers them
		}
		stubDialer(cfg, "28P01")
		_, err = pgconn.ConnectConfig(context.Background(), cfg)
		got := wrapConnError(err, c.dsn, "pgvector: connect")
		if !cascade.HasKind(got, cascade.KindPermissionDenied) {
			t.Errorf("%s: kind = %s, want KindPermissionDenied", c.name, kindOf(got))
		}
		assertNoLeak(t, c.name, got, c.forbid)
		checked++
	}
	if checked == 0 {
		t.Fatal("no adversarial DSN parsed, so nothing was checked")
	}
	t.Logf("checked %d stub auth failures", checked)
}
