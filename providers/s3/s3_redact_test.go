// Purpose: proof that redactEndpoint, now routed through
// providers/internal/dsnredact, never shows an endpoint credential: a
// userinfo password is masked, every query value is dropped, and a URL it
// cannot render safely (an '@' past the authority after '/', '?' or '#',
// no "scheme://", unparseable, another scheme) gives the placeholder. The
// bare canary and a password's leading segment are forbidden; Open's parse
// failure is held to the same list. Nothing is dialed.

package s3

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// s3Canary returns the acceptance password, assembled at run time.
func s3Canary() string { return strings.Join([]string{"s3", "cr3t"}, "") }

// placeholder is dsnredact's fixed rendering of an unsafe URL.
const placeholder = "<redacted-dsn>"

func TestRedactEndpointFailsClosed(t *testing.T) {
	c := s3Canary()
	for _, tc := range []struct{ name, raw, want, seg string }{
		{"userinfo", "http://u:" + c + "@127.0.0.1:9000", "http://u:xxxxx@127.0.0.1:9000", "u:" + c},
		{"userinfo-https-path", "https://u:" + c + "@h:9443/root", "https://u:xxxxx@h:9443/root", "u:" + c},
		{"query-password", "http://h:9000/?password=" + c, "http://h:9000/", "password="},
		{"query-any-secret", "https://h:9443/?X-Amz-Credential=" + c + "&token=" + c, "https://h:9443/", "X-Amz"},
		{"fragment", "http://u:" + c + "@h:9000/#" + c, "http://u:xxxxx@h:9000/", "#"},
		{"slash-password-is-port", "http://127.0.0.1:42451/" + c + "@h", placeholder, "42451"},
		{"query-mark-password-is-port", "http://127.0.0.1:42452?" + c + "@h", placeholder, "42452"},
		{"hash-password-is-port", "https://u:42453#" + c + "@h", placeholder, "42453"},
		{"single-slash", "http:/u:" + c + "@h:9000", placeholder, "u:"},
		{"opaque", "http:u:" + c + "@h", placeholder, "u:"},
		{"malformed", "http://u:" + c + "@[::1", placeholder, "u:"},
		{"scheme-not-listed", "ftp://u:" + c + "@h:21", placeholder, "u:"},
	} {
		got := redactEndpoint(tc.raw)
		if n := countHits(got, []string{c, tc.seg, tc.raw}); n != 0 {
			t.Errorf("%s: redactEndpoint output holds %d forbidden values", tc.name, n)
			continue
		}
		if got != tc.want { // safe to print: got holds no forbidden value
			t.Errorf("%s: redactEndpoint = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOpenParseFailureNeverEchoesEndpointPassword(t *testing.T) {
	c := s3Canary()
	for _, raw := range []string{
		"http:/u:" + c + "@h:9000",
		"http://u:" + c + "@[::1",
		"https:///u:" + c + "@h",
	} {
		_, err := Open(context.Background(), raw, "bucket", "id", "secret")
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Open(unparseable endpoint) kind is not KindInvalidInput (err=%v)", err != nil)
		}
		if n := countHits(errorTexts(err), []string{c, "u:" + c, raw}); n != 0 {
			t.Errorf("Open(unparseable endpoint) error holds %d forbidden values", n)
		}
		if !strings.Contains(err.Error(), placeholder) {
			t.Error("Open(unparseable endpoint) must show the placeholder")
		}
	}
}

// TestParseEndpointKeepsHostAndHidesUserinfo proves the parse step that
// feeds redactEndpoint: an https endpoint with a userinfo password yields
// its host and TLS, and a host-less one fails with the placeholder only.
func TestParseEndpointKeepsHostAndHidesUserinfo(t *testing.T) {
	c := s3Canary()
	host, secure, err := parseEndpoint("https://u:" + c + "@h:9443/root")
	if err != nil || host != "h:9443" || !secure {
		t.Fatalf("parseEndpoint(https) = host ok %v, secure %v, err %v", host == "h:9443", secure, err != nil)
	}
	_, _, err = parseEndpoint("https:///u:" + c + "@h")
	if err == nil || countHits(errorTexts(err), []string{c, "u:" + c}) != 0 || !strings.Contains(err.Error(), placeholder) {
		t.Error("parseEndpoint(host-less) must fail showing only the placeholder")
	}
}

// errorTexts renders err with %v, %+v, %#v and the Error() and %#v of
// every error on its Unwrap chain.
func errorTexts(err error) string {
	texts := []string{fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)}
	for e := err; e != nil; e = errors.Unwrap(e) {
		texts = append(texts, e.Error(), fmt.Sprintf("%#v", e))
	}
	return strings.Join(texts, "\n")
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
