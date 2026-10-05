//go:build integration

// Purpose: the live leg of redis_ambiguous_test.go. Open dials a loopback
// port nothing listens on for every password-is-port and single-slash
// form, so the error is go-redis's own refused dial, whose address would
// print the password's head; and a real refused net.Dial error goes
// through wrapConnError. The bare canary, the head and the URL are
// forbidden. No server is needed.

package redis

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestRedisOpenAmbiguousURLRefusedDialNeverEchoesPassword(t *testing.T) {
	c, p := redisCanary(), closedLoopbackPort(t)
	for name, raw := range ambiguousURLs(p, c) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		assertOpenHidesPassword(ctx, t, name, raw, p, c)
		cancel()
	}
	conn, dialErr := net.Dial("tcp", "127.0.0.1:"+p)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("dial to a closed loopback port succeeded")
	}
	if !strings.Contains(dialErr.Error(), p) {
		t.Fatal("the real dial error must name the port the forms forbid")
	}
	for name, raw := range ambiguousURLs(p, c) {
		err := wrapConnError(dialErr, raw, "redis.Open: connecting to")
		if n := countHits(errorTexts(err), []string{c, p, raw}); n != 0 {
			t.Errorf("%s: the wrapped real dial error holds %d forbidden values", name, n)
		}
	}
}
