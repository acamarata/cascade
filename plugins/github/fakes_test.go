package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/github/auth"
	"github.com/acamarata/cascade/plugins/github/tools"
)

// Purpose (this file): the test doubles the plugin's seams take, and the
//   constructor every test builds an instance with.
// Constraints: nothing here opens a socket or imports net/http — Art.7.2's
//   default unit lane forbids it, which is exactly why the transport, the
//   token poster and the callback listener are all injected seams.
// SPORT: plugins/github tests (ADD) — P1-E25-W5-S51-T1.

// fakeDoer answers API calls from canned responses and records what it was
// asked for, so a test can assert the call that WOULD have gone out.
type fakeDoer struct {
	status int
	body   []byte
	err    error

	calls []tools.Request
	token string
}

func (f *fakeDoer) Do(_ context.Context, req tools.Request, token string) (int, []byte, error) {
	f.calls = append(f.calls, req)
	f.token = token
	if f.err != nil {
		return 0, nil, f.err
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return status, f.body, nil
}

// last returns the most recent request, failing if none was made.
func (f *fakeDoer) last(t *testing.T) tools.Request {
	t.Helper()
	if len(f.calls) == 0 {
		t.Fatal("no request reached the transport")
	}
	return f.calls[len(f.calls)-1]
}

// fakeWaiter stands in for the loopback listener: it yields a prepared
// callback query without binding anything.
type fakeWaiter struct {
	query  url.Values
	err    error
	closed bool
}

func (f *fakeWaiter) RedirectURI() string { return "http://127.0.0.1:54321/callback" }

func (f *fakeWaiter) Wait(_ context.Context, p auth.PKCE) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	// Route through the real verifier so the state check this plugin
	// depends on is exercised, not bypassed.
	return auth.VerifyCallback(f.query, p)
}

func (f *fakeWaiter) Close() error {
	f.closed = true
	return nil
}

// fakePoster answers the token endpoint.
type fakePoster struct {
	status int
	body   string
	err    error

	form url.Values
}

func (f *fakePoster) post(_ context.Context, _ string, form url.Values) (int, []byte, error) {
	f.form = form
	if f.err != nil {
		return 0, nil, f.err
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return status, []byte(f.body), nil
}

// newTestPlugin builds an instance wired to doubles. The waiter is returned
// so a test can assert the listener was closed.
func newTestPlugin(doer tools.Doer, waiter *fakeWaiter, poster *fakePoster) (*broker, *strings.Builder) {
	var out strings.Builder
	p := &broker{doer: doer, out: &out}
	if poster != nil {
		p.post = poster.post
	}
	if waiter != nil {
		p.flows.listen = func() (callbackWaiter, error) { return waiter, nil }
	}
	return p, &out
}

// errTransport is a transport failure, distinct from an HTTP error status.
var errTransport = errors.New("dial refused")
