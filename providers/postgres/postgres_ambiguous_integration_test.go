//go:build postgres && integration

// Purpose: proof that providers/postgres's Open never echoes a password
// holding an unescaped '/', '?' or '#' after a numeric head, or a DSN
// without "postgres://": pgx reads the head as the port and the tail as
// the database, and a refused dial prints both (host:port, database=...).
// Open dials a loopback port nothing listens on; the bare canary and the
// head are forbidden alongside the DSN, in every fmt verb and Unwrap step.
// The reconnect path (wrapDBError over a real refused ConnectError) and
// Driver.String are held to the same list. Integration-tagged: it dials
// (loopback only, no server needed); the unit leg is the shared DSN
// table's is-port rows.

package postgres

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// closedLoopbackPort returns a loopback TCP port nothing listens on: it
// binds an ephemeral port and closes the listener at once.
func closedLoopbackPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

// ambiguousDSNs lists the password-is-port and single-slash forms around
// the closed loopback port p and the canary c.
func ambiguousDSNs(p, c string) map[string]string {
	return map[string]string{
		"slash-loopback":          "postgres://127.0.0.1:" + p + "/" + c + "@localhost/db",
		"slash-localhost":         "postgres://localhost:" + p + "/" + c + "@localhost/db",
		"slash-empty-host":        "postgres://:" + p + "/" + c + "@h/db",
		"query-mark-loopback":     "postgres://127.0.0.1:" + p + "?" + c + "@localhost/db",
		"hash-loopback":           "postgres://127.0.0.1:" + p + "#" + c + "@h/db",
		"single-slash":            "postgres:/u:" + c + "@127.0.0.1:" + p + "/db",
		"single-slash-query-mark": "postgres:/u:" + p + "?" + c + "@127.0.0.1/db",
	}
}

func TestPostgresOpenAmbiguousDSNNeverEchoesPassword(t *testing.T) {
	isolatePGEnv(t)
	c, p := canary(), closedLoopbackPort(t)
	rawHits := 0
	for name, dsn := range ambiguousDSNs(p, c) {
		forbid := []string{c, p, dsn}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := Open(ctx, dsn)
		assertNoLeak(t, name+"/open", err, forbid)
		if _, perr := pgconn.ParseConfig(dsn); perr == nil {
			_, raw := pgconn.Connect(ctx, dsn) // the real refused dial a pooled reconnect would surface
			rawHits += countHits(raw.Error(), forbid[:2])
			assertNoLeak(t, name+"/reconnect", wrapDBError(raw, "postgres: get %s", "ns"), forbid)
			assertNoLeak(t, name+"/wrap", wrapConnError(raw, dsn, "postgres: connect"), forbid)
		}
		if countHits(newDriver(nil, dsn).String(), forbid) != 0 {
			t.Errorf("%s: Driver.String holds a forbidden value", name)
		}
		cancel()
	}
	if rawHits == 0 {
		t.Fatal("no raw refused dial holds the canary or the leading segment: the forms no longer prove the leak shape")
	}
	t.Logf("raw refused dials holding the canary or the leading segment: %d", rawHits)
}
