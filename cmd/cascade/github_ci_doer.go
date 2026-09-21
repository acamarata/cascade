// Purpose: the CONCRETE net/http adapter wait-on-green's GitHub polling
//
//	runs on -- one authenticated, fully buffered GitHub REST request with
//	the response headers the ETag cache needs.
//
// WHY IT LIVES HERE AND NOT IN internal/ci. poll.go's header states the
//
//	split that package keeps to: "a Doer (production: a *http.Client
//	adapter built by the composition root, matching
//	internal/providers/intake/transport.go's own 'Doer interface here,
//	concrete net/http adapter in the composition root' split -- this
//	package never imports net/http, so it never needs an internal/build
//	egress-allowlist entry of its own)". The adapter was briefly built in
//	internal/ci/waitmerge_deps.go instead, which broke exactly that: it
//	turned internal/build's TestArchEgressImportAllowlist red (the
//	network list does not name internal/ci) and forced its unit test to
//	import net/http, which TestNoNetworkUnitTest forbids. cmd/cascade IS
//	on the network list -- "the composition root builds the client and
//	daemon transports" -- so the adapter belongs here.
//
//	It is a file of its own rather than more of github_ci_cmd.go only
//	because that file is at Art.10.3's 300-line cap.
//
// Inputs: a resolved GitHub API token, and one ci.HTTPRequest per call.
//
// Outputs: a ci.HTTPResponse, or a TYPED cascade error -- KindInvalidInput
//
//	for a request that cannot be built, KindUnavailable for a transport
//	or body-read failure (the kind internal/ci's own
//	isTransientPollError retries).
//
// Constraints: the body is bounded (a hostile or malformed response must
//
//	not exhaust memory) and one request is bounded in time, independently
//	of the wait's own much longer budget.
//
// SPORT: cmd/cascade:github-ci-doer (ADD) -- P1-E25-W5-S51-T3.
package main

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/pkg/cascade"
)

// githubHTTPTimeout bounds one GitHub REST request. The overall wait has
// its own (much longer) budget; this one stops a single hung connection
// from consuming it.
const githubHTTPTimeout = 30 * time.Second

// githubMaxResponseBytes bounds one buffered response body.
const githubMaxResponseBytes = 8 << 20

// newGitHubTokenDoer is the ci.NewTokenDoer the two verbs inject.
func newGitHubTokenDoer(token string) ci.Doer {
	return &githubHTTPDoer{token: token, client: &http.Client{Timeout: githubHTTPTimeout}}
}

// githubHTTPDoer is the production ci.Doer.
type githubHTTPDoer struct {
	token  string
	client *http.Client
}

// Do implements ci.Doer.
func (d *githubHTTPDoer) Do(ctx context.Context, req ci.HTTPRequest) (ci.HTTPResponse, error) {
	hreq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, nil)
	if err != nil {
		return ci.HTTPResponse{}, cascade.Wrap(cascade.KindInvalidInput, err, "ci: building the GitHub request")
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	hreq.Header.Set("Authorization", "Bearer "+d.token)
	hreq.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := d.client.Do(hreq)
	if err != nil {
		return ci.HTTPResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: the GitHub request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxResponseBytes))
	if err != nil {
		return ci.HTTPResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: reading the GitHub response")
	}
	return ci.HTTPResponse{Status: resp.StatusCode, Header: githubFlattenHeader(resp.Header), Body: body}, nil
}

// githubFlattenHeader renders net/http's multi-valued header map as the
// single-valued one ci.HTTPResponse carries, keeping the FIRST value of
// each key (the one net/http itself returns from Header.Get).
//
// The parameter is the UNNAMED map type rather than http.Header, which
// http.Header is assignable to: that lets this function's own test stay
// free of a net/http import, which TestNoNetworkUnitTest requires of every
// unit test in the tree.
func githubFlattenHeader(h map[string][]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
