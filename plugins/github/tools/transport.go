package tools

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the real HTTP transport — the ONE place this plugin
//
//	opens a socket, and the only file in the package that imports net/http.
//
// Inputs: a described Request and a bearer token.
// Outputs: the response status and body, read to completion.
// Constraints: every request goes to APIBase and nowhere else — the URL is
//
//	built here from the request's path, never taken from a caller or from a
//	response field, so no redirect or injected value can move this plugin's
//	egress off the single host its manifest declares (net.http:api.github.com).
//	Isolating net/http here is what lets the rest of the package be tested
//	in the default unit lane, which Art.7.2 forbids from importing it.
//
// SPORT: plugins/github/tools:transport (ADD) — P1-E25-W5-S51-T1.

// RequestTimeout bounds a single API call.
//
// It is a per-call ceiling, not a retry budget: a GitHub call that has not
// answered in thirty seconds is not going to, and a tool invocation that
// hangs is worse than one that fails, because the caller cannot tell the
// difference between slow and stuck.
const RequestTimeout = 30 * time.Second

// MaxResponseBytes caps how much of a response is read.
//
// The cap exists because this plugin reads the whole body into memory to
// decode it. A response larger than this is refused rather than allowed to
// grow the process without bound — a listing endpoint pointed at a very
// large account, or a proxy returning something that is not GitHub at all.
const MaxResponseBytes = 32 << 20 // 32 MiB

// UserAgent identifies this plugin to GitHub. The API requires a
// User-Agent and rejects requests without one.
const UserAgent = "cascade-github"

// HTTPDoer performs requests against the real GitHub API.
type HTTPDoer struct {
	// Client is the underlying HTTP client. A zero HTTPDoer uses a client
	// with RequestTimeout, so the useful default needs no construction.
	Client *http.Client
}

// Do performs req and returns its status and body.
//
// Redirects are deliberately NOT followed: GitHub answers a moved
// repository with a redirect to a different path, and silently following it
// would make a call land somewhere the caller did not name — which for a
// merge or an issue close is the wrong repository being written to. The
// redirect comes back as its status and is classified like any other.
func (d HTTPDoer) Do(ctx context.Context, req Request, token string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	httpReq, err := d.build(ctx, req, token)
	if err != nil {
		return 0, nil, err
	}

	resp, err := d.client().Do(httpReq)
	if err != nil {
		return 0, nil, cascade.Wrap(cascade.KindUnavailable, err, "github: the API call failed")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return 0, nil, cascade.Wrap(cascade.KindUnavailable, err, "github: reading the API response")
	}
	if len(body) > MaxResponseBytes {
		return 0, nil, cascade.Newf(cascade.KindInvalidInput,
			"github: the response exceeded %d bytes", MaxResponseBytes)
	}
	return resp.StatusCode, body, nil
}

// build renders a described Request as an HTTP request against APIBase.
func (d HTTPDoer) build(ctx context.Context, req Request, token string) (*http.Request, error) {
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL(), body)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "github: building a %s request", req.Method)
	}
	for name, value := range APIHeaders(req, token) {
		httpReq.Header.Set(name, value)
	}
	return httpReq, nil
}

// APIHeaders is the header POLICY every GitHub call carries, separated from
// the plumbing that applies it so it can be asserted directly — net/http is
// unreachable from the default unit lane (Art.7.2), and these headers are
// the part worth pinning: the API version this plugin is written against,
// the User-Agent GitHub requires, and the rule that an absent token means
// an UNAUTHENTICATED call rather than an "Authorization: Bearer " header
// with nothing after it, which GitHub answers 401.
func APIHeaders(req Request, token string) map[string]string {
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           UserAgent,
	}
	if len(req.Body) > 0 {
		headers["Content-Type"] = "application/json"
	}
	if strings.TrimSpace(token) != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

// client returns the configured client, or the default one.
func (d HTTPDoer) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{
		Timeout: RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
