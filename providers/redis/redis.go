// Package redis is the Redis provider.Cache and provider.Queue driver
// (providers/redis/ per 02-TARGET-STRUCTURE §providers): the server
// profile's Cache and Queue legs (P1-E17-W4-S38-T6), mirroring
// providers/postgres's shape for the Store family and providers/pgvector's
// for VectorStore.
//
// Wire library: github.com/redis/go-redis/v9 (BSD-2-Clause), a pure-Go
// RESP client — no CGO in this package (06 §2). This file holds the
// shared connection: Open dials a real server (a URL naming its scheme,
// optional credentials, host, port and database index — 08 §2's env-ref
// rule keeps the literal out of any config file, and this file never
// echoes it back in an error), Close releases the pool, and classifyErr
// maps a go-redis error to the pkg/cascade taxonomy the Cache and Queue
// types in cache.go/queue.go both route their own errors through.
//
// Purpose: shared Redis connection lifecycle + error classification for
//
//	the Cache (cache.go) and Queue (queue.go) drivers in this package.
//
// Inputs: a connection URL (Open).
// Outputs: a *Conn wrapping a live client, or a *cascade.Error carrying a
//
//	taxonomy Kind.
//
// Constraints: providers/** imports pkg/** only, never internal/**
//
//	(Art.10.2); no CGO (06 §2); every credential-bearing value is
//	redacted before it can reach an error message (redactURL).
//
// SPORT: providers.redis/ADDED (P1-E17-W4-S38-T6).
package redis

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Conn is the shared, live Redis connection both the Cache and Queue
// drivers in this package wrap. The zero value is not usable; construct
// with Open.
type Conn struct {
	rdb *goredis.Client
}

// Open parses rawURL (a redis:// or rediss:// URL — see go-redis's
// ParseURL for the accepted shape) and dials a real Redis server,
// confirming reachability with a PING before returning. An empty rawURL,
// a URL go-redis cannot parse, or a server that fails to answer PING (down,
// wrong credentials, wrong port) all return a typed, fail-closed
// *cascade.Error — never a *Conn a caller could mistake for a working one.
func Open(ctx context.Context, rawURL string) (*Conn, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "redis.Open: url must not be empty")
	}
	opts, err := goredis.ParseURL(rawURL)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "redis.Open: parsing url")
	}
	rdb := goredis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, wrapConnError(err, rawURL, "redis.Open: connecting to")
	}
	return &Conn{rdb: rdb}, nil
}

// Close releases the underlying connection pool. Safe to call on a nil
// *Conn.
func (c *Conn) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// String returns a short diagnostic label. It never includes the URL this
// Conn was opened with (which may carry credentials).
func (c *Conn) String() string { return "redis.Conn" }

// classifyErr reports the taxonomy Kind that best fits a go-redis client
// error. Classification reads the error's structured shape (context
// deadline/cancellation, a *net.OpError, go-redis's own auth-failure
// sentinel), never the message text, matching
// providers/postgres/postgres_errors.go's classify-by-structure precedent.
func classifyErr(err error) cascade.Kind {
	switch {
	case err == nil:
		return cascade.KindInternal
	case errors.Is(err, context.DeadlineExceeded):
		return cascade.KindTimeout
	case errors.Is(err, context.Canceled):
		return cascade.KindCanceled
	case isAuthError(err):
		return cascade.KindPermissionDenied
	default:
		var netErr *net.OpError
		if errors.As(err, &netErr) {
			return cascade.KindUnavailable
		}
		return cascade.KindUnavailable
	}
}

// isAuthError reports whether err is go-redis's own reported authentication
// failure — it returns the server's RESP error reply as a plain *goredis.Error
// (a string-typed error with no structured code), so this is judged by the
// well-known RESP prefixes Redis itself uses for the two auth-failure
// replies, not by pattern-matching an arbitrary message.
func isAuthError(err error) bool {
	var redisErr goredis.Error
	if !errors.As(err, &redisErr) {
		return false
	}
	msg := redisErr.Error()
	return strings.HasPrefix(msg, "NOAUTH") || strings.HasPrefix(msg, "WRONGPASS") ||
		strings.HasPrefix(msg, "ERR invalid password")
}

// wrapConnError wraps a connection-establishment error (Open's Ping),
// taking rawURL only to compute its redacted form for the message — the
// raw URL itself is never interpolated (08 §2's credential-custody rule
// extended to error messages, matching providers/postgres's wrapConnError).
func wrapConnError(err error, rawURL, msg string) error {
	return cascade.Wrapf(classifyErr(err), err, "%s %s", msg, redactURL(rawURL))
}

// redactedURLPlaceholder stands in for a URL net/url cannot parse at all,
// so an unparseable (and therefore unpredictable-shape) URL still cannot
// leak a credential fragment into an error message.
const redactedURLPlaceholder = "redis://<redacted>"

// redactURL returns rawURL with any userinfo password replaced via the
// standard-library net/url.URL.Redacted() method, matching
// providers/postgres/postgres_errors.go's redactDSN.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return redactedURLPlaceholder
	}
	return u.Redacted()
}
