package tools

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): perform a tool call end to end — build the request,
//
//	send it, map the status onto the error taxonomy, decode the body.
//
// Inputs: a tool name, its Args, and a Doer that performs one request.
// Outputs: the decoded value for that tool, or a typed error.
// Constraints: the Doer seam is expressed in PLAIN VALUES (method, path,
//
//	bytes, status) rather than net/http types. That is deliberate: Art.7.2's
//	gate forbids an untagged _test.go from importing "net" or "net/http" at
//	all, so a seam typed in *http.Request would make this package's own
//	tests impossible to write in the default unit lane. net/http lives in
//	transport.go and nowhere else.
//
// SPORT: plugins/github/tools:client (ADD) — P1-E25-W5-S51-T1.

// Doer performs one described request and reports what came back.
//
// A transport error (the host is unreachable, the context expired) is
// returned as err. An HTTP error STATUS is not: it comes back as a status
// with its body, because GitHub's error body is what makes a 403 readable,
// and Classify needs both.
type Doer interface {
	Do(ctx context.Context, req Request, token string) (status int, body []byte, err error)
}

// Client performs this plugin's tool calls against the GitHub API.
//
// The token is held in memory for the life of the process and is never
// written to a file or a log line; it reaches this struct from the OAuth
// exchange, and the vault's copy is stored by the host.
type Client struct {
	// Doer performs requests. A nil Doer is a programming error and is
	// refused rather than panicking mid-call.
	Doer Doer
	// Token authorizes the call. An empty token is allowed: GitHub serves
	// public data unauthenticated, at a lower rate limit.
	Token string
}

// Call runs one tool and returns its decoded result.
//
// The order is the contract: build (which validates arguments before any
// egress), send, classify, decode. A request that cannot be built never
// reaches the network, so a typo in an owner name costs nothing and a
// malformed merge never touches a branch.
func (c Client) Call(ctx context.Context, tool string, a Args) (any, error) {
	if c.Doer == nil {
		return nil, cascade.New(cascade.KindInternal, "github: the client has no transport")
	}
	req, err := BuildRequest(tool, a)
	if err != nil {
		return nil, err
	}
	status, body, err := c.Doer.Do(ctx, req, c.Token)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "github: calling %s", req.URL())
	}
	if err := Classify(status, body); err != nil {
		return nil, err
	}
	return decodeFor(tool, body)
}

// decoders maps a tool onto the decoder for its response shape.
//
// It is a table rather than a switch so that it cannot drift from
// BuildRequest's table without one of them being obviously shorter:
// TestEveryBuiltRequestHasADecoder asserts the two cover the same tools.
var decoders = map[string]func([]byte) (any, error){
	"repos.list":      func(b []byte) (any, error) { return DecodeRepos(b) },
	"repos.get":       func(b []byte) (any, error) { return DecodeRepo(b) },
	"repos.clone_url": decodeCloneURL,

	"issues.list":    func(b []byte) (any, error) { return DecodeIssues(b) },
	"issues.get":     func(b []byte) (any, error) { return DecodeIssue(b) },
	"issues.create":  func(b []byte) (any, error) { return DecodeIssue(b) },
	"issues.comment": func(b []byte) (any, error) { return DecodeIssueComment(b) },
	"issues.close":   func(b []byte) (any, error) { return DecodeIssue(b) },

	"prs.list":           func(b []byte) (any, error) { return DecodePullRequests(b) },
	"prs.get":            func(b []byte) (any, error) { return DecodePullRequest(b) },
	"prs.create":         func(b []byte) (any, error) { return DecodePullRequest(b) },
	"prs.merge":          func(b []byte) (any, error) { return DecodeMergeResult(b) },
	"prs.review_request": func(b []byte) (any, error) { return DecodePullRequest(b) },
}

// decodeFor decodes body as the response shape tool returns.
func decodeFor(tool string, body []byte) (any, error) {
	decode, ok := decoders[tool]
	if !ok {
		return nil, cascade.Newf(cascade.KindInternal,
			"github: %s has no response decoder", ToolNamespace+"."+tool)
	}
	return decode(body)
}

// decodeCloneURL turns a repository record into the one string the
// clone_url tool exists to produce. The request is a plain repository
// fetch; the tool is the projection of it.
func decodeCloneURL(body []byte) (any, error) {
	repo, err := DecodeRepo(body)
	if err != nil {
		return nil, err
	}
	return CloneURL(repo)
}
