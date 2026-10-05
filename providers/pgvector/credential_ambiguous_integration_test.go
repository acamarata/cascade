//go:build postgres && integration

// Purpose: proof that pgvector's Open never echoes a password holding an
// unescaped '/', '?' or '#' after a numeric head, or a DSN without
// "postgres://": pgx reads the head as the port and the tail as the
// database, and a refused dial prints both (host:port, database=...). Open
// dials a loopback port nothing listens on; the bare canary and the head
// are forbidden alongside the DSN, in every fmt verb and Unwrap step. The
// reconnect path (wrapDBError over a real refused ConnectError) is held to
// the same list. Integration-tagged: it dials (loopback only, no server
// needed); the unit leg is the shared DSN table's is-port rows.

package pgvector

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/acamarata/cascade/providers/internal/dsnredact"
)

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

func TestPgvectorOpenAmbiguousDSNNeverEchoesPassword(t *testing.T) {
	isolatePGEnv(t)
	c, p := canary(), strconv.Itoa(closedLoopbackPort(t))
	rawHits := 0
	for name, dsn := range ambiguousDSNs(p, c) {
		forbid := []string{c, p, dsn}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := Open(ctx, dsn)
		assertNoLeak(t, name+"/open", err, forbid)
		if _, perr := pgconn.ParseConfig(dsn); perr == nil {
			_, raw := pgconn.Connect(ctx, dsn) // the real refused dial a pooled reconnect would surface
			rawHits += countHits(raw.Error(), forbid[:2])
			secrets, _ := dsnredact.Secrets(dsn)
			assertNoLeak(t, name+"/reconnect", wrapDBError(raw, secrets, "pgvector: count %s", "ns"), forbid)
			assertNoLeak(t, name+"/wrap", wrapConnError(raw, dsn, "pgvector: connect"), forbid)
		}
		cancel()
	}
	if rawHits == 0 {
		t.Fatal("no raw refused dial holds the canary or the leading segment: the forms no longer prove the leak shape")
	}
	t.Logf("raw refused dials holding the canary or the leading segment: %d", rawHits)
}
