// Purpose: unit proof that a redis URL whose password holds an unescaped
// '#', '?' or '/' after a numeric head, or that lacks "redis://", never
// puts the password or its head in an Open or connection error. go-redis
// reads the head as the port, so a dial error's address would print it;
// the bare canary and the head are forbidden alongside the URL. Nothing is
// dialed here (the no-network unit gate): the dial error is a string of
// the shape go-redis returns, and Open runs on a canceled context. The
// real refused dial is in redis_ambiguous_integration_test.go.

package redis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ambiguousURLs lists the password-is-port and single-slash forms around
// the port p and the canary c.
func ambiguousURLs(p, c string) map[string]string {
	return map[string]string{
		"hash":                    "redis://127.0.0.1:" + p + "#" + c + "@h/0",
		"hash-empty-host":         "redis://:" + p + "#" + c + "@h/0",
		"hash-user":               "redis://127.0.0.1:" + p + "#u:" + c + "@h/0",
		"query-mark":              "redis://127.0.0.1:" + p + "?" + c + "@h/0",
		"slash":                   "redis://127.0.0.1:" + p + "/" + c + "@h/0",
		"single-slash":            "redis:/u:" + c + "@127.0.0.1:" + p,
		"single-slash-db":         "redis:/u:" + c + "@127.0.0.1:" + p + "/0",
		"single-slash-query-mark": "redis:/u:" + p + "?" + c + "@127.0.0.1",
	}
}

// assertOpenHidesPassword runs Open(ctx, raw) and fails when it succeeds,
// when its Kind is not Unavailable, InvalidInput or Canceled, or when any
// rendering of the error holds c, the leading segment p or raw.
func assertOpenHidesPassword(ctx context.Context, t *testing.T, name, raw, p, c string) {
	t.Helper()
	conn, err := Open(ctx, raw)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("%s: Open succeeded, want a connection or parse error", name)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) && !cascade.HasKind(err, cascade.KindInvalidInput) &&
		!cascade.HasKind(err, cascade.KindCanceled) {
		t.Errorf("%s: unexpected Kind for a failed Open", name)
	}
	if n := countHits(errorTexts(err), []string{c, p, raw}); n != 0 {
		t.Errorf("%s: the Open error holds %d forbidden values (canary, leading segment, URL)", name, n)
	}
}

func TestRedisOpenAmbiguousURLNeverEchoesPassword(t *testing.T) {
	c, p := redisCanary(), "46381"
	canceled, cancel := context.WithCancel(context.Background())
	cancel() // a URL go-redis parses must not dial here
	for name, raw := range ambiguousURLs(p, c) {
		assertOpenHidesPassword(canceled, t, name, raw, p, c)
	}
}

func TestRedisConnErrorWithholdsADialThatPrintsThePassword(t *testing.T) {
	c, p := redisCanary(), "46382"
	dialErr := errors.New("dial tcp 127.0.0.1:" + p + ": connect: connection refused")
	for name, raw := range ambiguousURLs(p, c) {
		err := wrapConnError(dialErr, raw, "redis.Open: connecting to")
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s: want KindUnavailable classified on the raw dial error", name)
		}
		if n := countHits(errorTexts(err), []string{c, p, raw}); n != 0 {
			t.Errorf("%s: the wrapped error holds %d forbidden values (canary, leading segment, URL)", name, n)
		}
	}
	clean := wrapConnError(dialErr, "redis://u:"+c+"@127.0.0.1:"+p+"/0", "redis.Open: connecting to")
	if !strings.Contains(errorTexts(clean), dialErr.Error()) || strings.Contains(errorTexts(clean), c) {
		t.Error("a URL that renders must keep the clean dial text and never the password")
	}
}

// countHits counts the forbidden values text holds, so a failure reports a
// number and never a canary.
func countHits(text string, forbidden []string) (n int) {
	for _, f := range forbidden {
		if f != "" && strings.Contains(text, f) {
			n++
		}
	}
	return n
}
