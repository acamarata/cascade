// Purpose: pin the OAuth transport's redirect table, exact size cap and listener edges in the default lane.
// Constraints: no "net"/"net/http" import and no tag. The token client gets a RoundTripper whose types
// are inferred (generics plus reflection); the listener edges use the production loopback seam.
// SPORT: OAUTH_BROKER: ADD (transport edge tests, default lane).

package secrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rtCall is what the fake round tripper saw; rtReply is what it answers.
type rtCall struct {
	method, body string
	target       *url.URL
	header       map[string][]string
}
type rtReply struct {
	status int
	header map[string][]string
	body   io.Reader
	err    error
}

// fakeRT is an http.RoundTripper whose Request/Response types are inferred.
type fakeRT[Req, Resp any] struct {
	reply func(rtCall) rtReply
	calls *[]rtCall
}

func (f fakeRT[Req, Resp]) RoundTrip(r Req) (Resp, error) {
	var zero Resp
	v := reflect.ValueOf(r).Elem()
	body, _ := io.ReadAll(v.FieldByName("Body").Interface().(io.Reader))
	hdr := v.FieldByName("Header").Convert(reflect.TypeOf(map[string][]string(nil))).Interface()
	call := rtCall{method: v.FieldByName("Method").String(), target: v.FieldByName("URL").Interface().(*url.URL),
		header: hdr.(map[string][]string), body: string(body)}
	*f.calls = append(*f.calls, call)
	rep := f.reply(call)
	if rep.err != nil {
		return zero, rep.err
	}
	resp := reflect.New(reflect.TypeOf(zero).Elem())
	e := resp.Elem()
	e.FieldByName("StatusCode").SetInt(int64(rep.status))
	if rep.header == nil {
		rep.header = map[string][]string{}
	}
	e.FieldByName("Header").Set(reflect.ValueOf(rep.header).Convert(e.FieldByName("Header").Type()))
	e.FieldByName("Body").Set(reflect.ValueOf(io.NopCloser(rep.body)))
	return resp.Interface().(Resp), nil
}

func newFakeRT[Req, Resp any](_ func(Req, []Req) error, _ func(string) (Resp, error), reply func(rtCall) rtReply) (fakeRT[Req, Resp], *[]rtCall) {
	calls := &[]rtCall{}
	return fakeRT[Req, Resp]{reply: reply, calls: calls}, calls
}

// exchangerWith returns the production exchanger with its transport replaced.
func exchangerWith(reply func(rtCall) rtReply) (*httpExchanger, *[]rtCall) {
	ex := newHTTPExchanger().(*httpExchanger)
	rt, calls := newFakeRT(ex.client.CheckRedirect, ex.client.Get, reply)
	ex.client.Transport = rt
	return ex, calls
}

// aReader is an endless stream of 'a' that counts what it gave.
type aReader struct{ given int }

func (a *aReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	a.given += len(p)
	return len(p), nil
}

var errMidBody = errors.New("connection reset mid-body")

func TestExchangeBuildsTheFormRequestAndNeverFollowsRedirects(t *testing.T) {
	ex, calls := exchangerWith(func(rtCall) rtReply { return rtReply{status: 200, body: strings.NewReader(`{"ok":1}`)} })
	if ex.client.Timeout != exchangeTimeout {
		t.Fatalf("client timeout %v, want %v", ex.client.Timeout, exchangeTimeout)
	}
	if err := refuseRedirect(nil, nil); err == nil || !strings.Contains(err.Error(), "use last response") {
		t.Fatalf("refuseRedirect returned %v, want the use-last-response sentinel", err)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code_verifier": {"v-" + canaryCode}}
	body, status, err := ex.Exchange(context.Background(), "https://idp.example/token", form)
	if err != nil || status != 200 || string(body) != `{"ok":1}` {
		t.Fatalf("Exchange = %q/%d/%v", body, status, err)
	}
	c := (*calls)[0]
	if len(*calls) != 1 || c.method != "POST" || c.target.String() != "https://idp.example/token" || c.body != form.Encode() {
		t.Fatalf("wire request wrong: %d calls, %+v", len(*calls), c)
	}
	if c.header["Content-Type"][0] != "application/x-www-form-urlencoded" || c.header["Accept"][0] != "application/json" {
		t.Fatalf("headers wrong: %v", c.header)
	}
}

func TestExchangeRefusesEveryRedirectStatusAndNeverResendsTheForm(t *testing.T) {
	const target = "https://evil.example/steal"
	for _, status := range []int{300, 301, 302, 303, 304, 305, 307, 308, 399} {
		for _, withLocation := range []bool{true, false} {
			t.Run(strconv.Itoa(status)+"/location="+strconv.FormatBool(withLocation), func(t *testing.T) {
				var hdr map[string][]string
				if withLocation {
					hdr = map[string][]string{"Location": {target}}
				}
				ex, calls := exchangerWith(func(rtCall) rtReply {
					return rtReply{status: status, header: hdr, body: strings.NewReader(`{"error":"invalid_grant"}`)}
				})
				body, got, err := ex.Exchange(context.Background(), "https://idp.example/token", url.Values{"refresh_token": {canaryRefresh}})
				if !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, errOAuthRedirectRefused) {
					t.Fatalf("HTTP %d: err %v is not the unavailable redirect refusal", status, err)
				}
				if !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) || strings.Contains(err.Error(), target) {
					t.Fatalf("HTTP %d: message %q must name the status and not the Location", status, err)
				}
				if body != nil || got != status || len(*calls) != 1 {
					t.Fatalf("HTTP %d: body %q status %d calls %d; want nil, %d, 1", status, body, got, len(*calls), status)
				}
			})
		}
	}
	for _, status := range []int{200, 299, 400, 500} { // the table's edges: not redirects
		ex, _ := exchangerWith(func(rtCall) rtReply { return rtReply{status: status, body: strings.NewReader(`{"e":1}`)} })
		if body, got, err := ex.Exchange(context.Background(), "https://idp.example/token", url.Values{}); err != nil || got != status || string(body) != `{"e":1}` {
			t.Fatalf("HTTP %d: got %q/%d/%v, want the body and status untouched", status, body, got, err)
		}
	}
}

func TestExchangeSizeCapIsExact(t *testing.T) {
	run := func(r io.Reader) ([]byte, int, error) {
		ex, _ := exchangerWith(func(rtCall) rtReply { return rtReply{status: 200, body: r} })
		return ex.Exchange(context.Background(), "https://idp.example/token", url.Values{})
	}
	for _, n := range []int64{0, maxTokenResponseBytes - 1, maxTokenResponseBytes} {
		body, status, err := run(io.LimitReader(&aReader{}, n))
		if err != nil || status != 200 || !bytes.Equal(body, bytes.Repeat([]byte("a"), int(n))) {
			t.Fatalf("a %d byte body (cap %d) was refused or altered: len %d err %v", n, maxTokenResponseBytes, len(body), err)
		}
	}
	for _, n := range []int64{maxTokenResponseBytes + 1, 1 << 40} { // one over, then endless
		src := &aReader{}
		body, status, err := run(io.LimitReader(src, n))
		if !cascade.HasKind(err, cascade.KindIntegrity) || !errors.Is(err, errOAuthResponseTooLarge) {
			t.Fatalf("a body of %d bytes (cap %d): err %v is not the integrity refusal", n, maxTokenResponseBytes, err)
		}
		if body != nil || status != 200 || !strings.Contains(err.Error(), strconv.Itoa(maxTokenResponseBytes)) {
			t.Fatalf("oversize refusal: body nil=%v status %d message %q", body == nil, status, err)
		}
		if src.given != maxTokenResponseBytes+1 {
			t.Fatalf("read %d bytes, want exactly cap+1", src.given)
		}
	}
}

func TestExchangeClassifiesTransportAndBodyReadFailures(t *testing.T) {
	ex, _ := exchangerWith(func(rtCall) rtReply { return rtReply{err: errMidBody} })
	if body, status, err := ex.Exchange(context.Background(), "https://idp.example/token", url.Values{}); body != nil ||
		status != 0 || !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, errMidBody) {
		t.Fatalf("a transport failure was %q/%d/%v, want nil/0/unavailable wrapping the cause", body, status, err)
	}
	broken := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errMidBody))
	ex, _ = exchangerWith(func(rtCall) rtReply { return rtReply{status: 200, body: broken} })
	body, status, err := ex.Exchange(context.Background(), "https://idp.example/token", url.Values{})
	if body != nil || status != 200 || !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, errMidBody) ||
		!strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("a mid-body failure was %q/%d/%v, want nil/200/unavailable \"could not be read\"", body, status, err)
	}
}

// startListener binds the production listener and returns it with a GET
// helper that reports the status, or -1 when the request failed.
func startListener(t *testing.T, redirectURI string) (callbackListener, func(path string) int) {
	t.Helper()
	l, err := loopbackFactoryFor(redirectURI)(context.Background())
	if err != nil {
		t.Fatalf("binding the listener: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	client := newHTTPExchanger().(*httpExchanger).client
	return l, func(path string) int {
		resp, err := client.Get("http://127.0.0.1:" + l.Port() + path)
		if err != nil {
			return -1
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
}

// waitFor runs Wait under a deadline.
func waitFor(l callbackListener, d time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return l.Wait(ctx)
}

func TestLoopbackListenerPathRulesAndSlot(t *testing.T) {
	l, get := startListener(t, "http://127.0.0.1/callback")
	for _, path := range []string{"/callback/", "/Callback", "/callbackx", "/callback/extra", "/", "/favicon.ico"} {
		if got := get(path + "?code=stolen"); got != 404 {
			t.Fatalf("GET %s answered %d, want 404", path, got)
		}
	}
	for _, q := range []string{"code=first", "code=second"} {
		if got := get("/callback?" + q); got != 200 {
			t.Fatalf("GET %s answered %d, want 200 even when the slot is full", q, got)
		}
	}
	if q, err := waitFor(l, 5*time.Second); err != nil || q != "code=first" {
		t.Fatalf("Wait = %q/%v: a near-miss took the slot or the first callback was displaced", q, err)
	}
	if q, err := waitFor(l, 100*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second Wait returned %q/%v; the dropped callback must not queue", q, err)
	}
	root, getRoot := startListener(t, "http://127.0.0.1") // no path in the redirect URI: answer at /
	if got := getRoot("/?code=root"); got != 200 {
		t.Fatalf("an empty redirect path answered %d at /, want 200", got)
	}
	if q, err := waitFor(root, 5*time.Second); err != nil || q != "code=root" {
		t.Fatalf("root Wait = %q/%v", q, err)
	}
}

// badAddr is a net.Addr whose String carries no port.
type badAddr string

func (b badAddr) Network() string { return "fake" }
func (b badAddr) String() string  { return string(b) }

// fakeListener is a net.Listener with inferred Addr/Conn types.
type fakeListener[A, C any] struct {
	addr     any
	closeErr error
}

func (f *fakeListener[A, C]) Accept() (c C, err error) { return c, errors.New("not served") }
func (f *fakeListener[A, C]) Close() error             { return f.closeErr }
func (f *fakeListener[A, C]) Addr() A                  { a, _ := f.addr.(A); return a }

func newFakeListener[A, C any](_ func() A, _ func() (C, error), addr any, closeErr error) *fakeListener[A, C] {
	return &fakeListener[A, C]{addr: addr, closeErr: closeErr}
}

func freshServer[S any](like S) S { return reflect.New(reflect.TypeOf(like).Elem()).Interface().(S) }

// bareListener binds the path-less production listener, closed at cleanup.
func bareListener(t *testing.T) *loopbackListener {
	t.Helper()
	l, err := listenLoopback(context.Background())
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*loopbackListener)
}

// fakeLoopback wraps a fake listener in a real loopbackListener value.
func fakeLoopback(t *testing.T, addr any, closeErr error) *loopbackListener {
	t.Helper()
	rl := bareListener(t)
	return &loopbackListener{listener: newFakeListener(rl.listener.Addr, rl.listener.Accept, addr, closeErr),
		server: freshServer(rl.server), path: "/", queries: make(chan string, 1), closed: make(chan struct{})}
}

func TestLoopbackListenerPortAndCloseEdges(t *testing.T) {
	if p := fakeLoopback(t, badAddr("no-port-here"), nil).Port(); p != "" {
		t.Fatalf("Port on an address with no port = %q, want empty", p)
	}
	err := fakeLoopback(t, badAddr("127.0.0.1:1"), errors.New("device busy")).Close()
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "could not be released") {
		t.Fatalf("a non-closed listener error was %v, want unavailable \"could not be released\"", err)
	}
	// An already-closed listener's error is the expected race, not a failure.
	spent := bareListener(t)
	_ = spent.Close()
	already := spent.listener.Close()
	if err := fakeLoopback(t, badAddr("127.0.0.1:1"), already).Close(); already == nil || err != nil {
		t.Fatalf("closed-listener case: second close err %v, fake Close err %v; want a non-nil error swallowed", already, err)
	}
}
