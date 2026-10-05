// Purpose: unit proof that redis connection and parse errors never carry a
// URL credential: userinfo and any password query value (any case) are
// redacted by dsnredact, and a URL go-redis cannot parse leaves no cause
// (net/url's parse error quotes the whole URL). Canaries are assembled at
// run time and never printed. Nothing is dialed.

package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// redisCanary returns the acceptance password, assembled at run time.
func redisCanary() string { return strings.Join([]string{"s3", "cr3t"}, "") }

// errorTexts renders err with %v, %+v, %#v and the Error() of every error
// on its Unwrap chain.
func errorTexts(err error) string {
	texts := []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)}
	for e := err; e != nil; e = errors.Unwrap(e) {
		texts = append(texts, e.Error(), fmt.Sprintf("%#v", e))
	}
	return strings.Join(texts, "\n")
}

func TestRedisConnErrorRedactsQueryPassword(t *testing.T) {
	c := redisCanary()
	pw1, pw2 := c+"1", c+"2"
	dial := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	for _, tc := range []struct{ name, raw, shown string }{
		{"query-password", "redis://localhost:6379/0?password=" + c, "redis://localhost:6379/0"},
		{"query-password-any-case", "redis://localhost:6379/0?db=0&PASSWORD=" + c, "redis://localhost:6379/0"},
		{"userinfo", "redis://u:" + c + "@localhost:6379/0", "redis://u:xxxxx@localhost:6379/0"},
		{"userinfo-and-query-differ", "rediss://u:" + pw1 + "@localhost:6380/1?password=" + pw2, "rediss://u:xxxxx@localhost:6380/1"},
		{"unix-socket", "unix://u:" + c + "@/tmp/redis.sock?password=" + c, "unix://u:xxxxx@/tmp/redis.sock"},
	} {
		err := wrapConnError(dial, tc.raw, "redis.Open: connecting to")
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s: want KindUnavailable", tc.name)
		}
		if strings.Contains(errorTexts(err), c) || strings.Contains(errorTexts(err), tc.raw) {
			t.Errorf("%s: the error holds the password or the raw URL", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.shown) {
			t.Errorf("%s: error %q does not show the redacted URL %q", tc.name, err.Error(), tc.shown)
		}
	}
}

func TestRedisOpenParseErrorNeverEchoesURL(t *testing.T) {
	c := redisCanary()
	for _, raw := range []string{
		"redis://u:" + c + "@[::1",
		"redis://u:" + c + "@localhost:99x/0",
		"redis://u:" + c + "%zz@localhost/0",
		"redis://localhost:6379/0?password=" + c + "&bogus_option=1",
	} {
		_, err := Open(context.Background(), raw)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Open(unparseable) kind is not KindInvalidInput (err=%v)", err != nil)
		}
		if strings.Contains(errorTexts(err), c) {
			t.Error("Open(unparseable) error holds the URL password")
		}
		if errors.Unwrap(err) != nil {
			t.Error("Open(unparseable) must keep no cause: net/url's error quotes the URL")
		}
	}
}
