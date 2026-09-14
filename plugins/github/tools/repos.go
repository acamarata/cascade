// Package tools implements cascade-github's MCP tool groups: repositories,
// issues and pull requests.
//
// Purpose: turn a tool call into a GitHub REST request, and a GitHub REST
//
//	response into a typed result or a typed error.
//
// Inputs: tool arguments, and the raw bytes the API returned.
// Outputs: decoded results, or a *cascade.Error carrying the Kind the HTTP
//
//	status maps to.
//
// Constraints: imports pkg/** and stdlib ONLY, never internal/** (Art.10.2).
//
//	Request construction is deliberately separated from execution: a
//	Request is an inert description, so every path, method and body this
//	plugin would send is asserted in unit tests without a socket. That
//	separation is also what lets this package's tests obey the tree-wide
//	no-network unit rule, which fails any non-integration test importing
//	net or net/http.
//
// SPORT: plugins/github tools (ADD) — P1-E25-W5-S51-T1.
package tools

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// APIBase is the GitHub REST root. It is the plugin's single declared net
// scope; nothing here ever builds a URL against another host.
const APIBase = "https://api.github.com"

// Request is one GitHub API call, described but not performed. Execution
// belongs to the caller's transport (client.go), which keeps this package's
// decision-making testable without a network.
type Request struct {
	// Method is the HTTP method.
	Method string
	// Path is the API path, already escaped, without the host.
	Path string
	// Body is the JSON request body, or nil for a bodiless call.
	Body []byte
}

// URL renders the absolute URL this request targets.
func (r Request) URL() string { return APIBase + r.Path }

// APIError is GitHub's error envelope. Its shape is fixed by the API, not
// by this plugin; error.404.json is a real captured instance.
type APIError struct {
	// Message is GitHub's human-readable reason.
	Message string `json:"message"`
	// DocumentationURL points at the endpoint's docs.
	DocumentationURL string `json:"documentation_url"`
	// Status is the status code, which GitHub repeats as a string in some
	// responses and omits in others.
	Status string `json:"status"`
}

// Classify maps an HTTP status and response body onto the frozen error
// taxonomy. It is the single place a status becomes a Kind, so a caller
// never has to reason about HTTP numbers.
//
// The mapping is deliberate rather than mechanical:
//   - 401 is PermissionDenied, the signal the stored token must be
//     refreshed or re-granted.
//   - 403 is QuotaExhausted ONLY when the body or headers say rate limit;
//     a plain 403 is a real permission refusal and must not be retried as
//     though waiting would help.
//   - 404 is NotFound, which for GitHub also covers a private repository
//     the token cannot see: the API deliberately does not distinguish
//     them, and neither can this plugin.
//   - 422 is InvalidInput: the request was understood and rejected.
func Classify(status int, body []byte) error {
	apiErr := DecodeAPIError(body)
	detail := apiErr.Message
	if detail == "" {
		detail = "no message"
	}
	switch {
	case status == 401:
		return cascade.Newf(cascade.KindPermissionDenied,
			"github: unauthorized (%s): the stored token is missing, expired or revoked", detail)
	case status == 403 && isRateLimited(detail):
		return cascade.Newf(cascade.KindQuotaExhausted, "github: rate limited (%s)", detail)
	case status == 403:
		return cascade.Newf(cascade.KindPermissionDenied,
			"github: forbidden (%s): the token lacks the scope this call needs", detail)
	case status == 404:
		return cascade.Newf(cascade.KindNotFound,
			"github: not found (%s): the resource does not exist, or the token cannot see it", detail)
	case status == 422:
		return cascade.Newf(cascade.KindInvalidInput, "github: rejected (%s)", detail)
	case status == 429:
		return cascade.Newf(cascade.KindQuotaExhausted, "github: rate limited (%s)", detail)
	case status >= 500:
		return cascade.Newf(cascade.KindUnavailable, "github: server error %d (%s)", status, detail)
	case status >= 400:
		return cascade.Newf(cascade.KindInvalidInput, "github: request failed with %d (%s)", status, detail)
	default:
		return nil
	}
}

// isRateLimited reports whether a 403's message is the rate-limit one.
// GitHub returns 403 for both "you are going too fast" and "you may not do
// this at all", and only the message separates them.
func isRateLimited(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "rate limit") || strings.Contains(lower, "abuse detection")
}

// DecodeAPIError decodes GitHub's error envelope, tolerating a body that is
// not one: an empty or non-JSON body yields a zero APIError rather than an
// error of its own, because the caller already knows the request failed and
// needs the status either way.
func DecodeAPIError(body []byte) APIError {
	var out APIError
	_ = json.Unmarshal(body, &out)
	return out
}

// Repo is the subset of a GitHub repository this plugin exposes. The API
// returns far more; decoding a subset is deliberate, and the captured
// fixture keeps every field so an added field cannot go unnoticed.
type Repo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	Description   string `json:"description"`
	Owner         User   `json:"owner"`
}

// User is the account shape GitHub nests inside most resources.
type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

// DecodeRepo decodes a single repository response.
func DecodeRepo(body []byte) (Repo, error) {
	var out Repo
	if err := json.Unmarshal(body, &out); err != nil {
		return Repo{}, cascade.Wrap(cascade.KindIntegrity, err, "github: decoding a repository response")
	}
	return out, nil
}

// DecodeRepos decodes a repository list response.
func DecodeRepos(body []byte) ([]Repo, error) {
	var out []Repo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "github: decoding a repository list response")
	}
	return out, nil
}

// ListReposRequest builds the request listing owner's repositories.
func ListReposRequest(owner string, perPage int) (Request, error) {
	if err := validSegment("owner", owner); err != nil {
		return Request{}, err
	}
	path := "/users/" + url.PathEscape(owner) + "/repos"
	if perPage > 0 {
		path += "?per_page=" + strconv.Itoa(perPage)
	}
	return Request{Method: "GET", Path: path}, nil
}

// GetRepoRequest builds the request fetching one repository.
func GetRepoRequest(owner, repo string) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	return Request{Method: "GET", Path: repoPath(owner, repo)}, nil
}

// CloneURL returns the clone URL the API reported for r, refusing a
// repository whose response carried none rather than assembling a URL this
// plugin guessed.
func CloneURL(r Repo) (string, error) {
	if r.CloneURL == "" {
		return "", cascade.Newf(cascade.KindNotFound,
			"github: repository %q reported no clone_url", r.FullName)
	}
	return r.CloneURL, nil
}

// repoPath renders /repos/{owner}/{repo} with both segments escaped.
func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// validSegments validates an owner/repo pair.
func validSegments(owner, repo string) error {
	if err := validSegment("owner", owner); err != nil {
		return err
	}
	return validSegment("repo", repo)
}

// validSegment refuses an empty or traversing path segment. A tool argument
// reaches a URL path directly, so this is the boundary that keeps a caller
// from addressing an endpoint this plugin never meant to expose.
func validSegment(field, value string) error {
	switch {
	case strings.TrimSpace(value) == "":
		return cascade.Newf(cascade.KindInvalidInput, "github: %s is empty", field)
	case strings.Contains(value, "/"), strings.Contains(value, ".."):
		return cascade.Newf(cascade.KindInvalidInput,
			"github: %s %q contains a path separator; it must name one %s only", field, value, field)
	default:
		return nil
	}
}

// ToolNamespace is the prefix every tool name this plugin exposes carries.
const ToolNamespace = "cascade-github"

// Args is the union of every argument the tool groups accept.
type Args struct {
	Owner         string   `json:"owner"`
	Repo          string   `json:"repo"`
	Number        int      `json:"number"`
	State         string   `json:"state"`
	PerPage       int      `json:"per_page"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`
	Labels        []string `json:"labels"`
	Head          string   `json:"head"`
	Base          string   `json:"base"`
	Draft         bool     `json:"draft"`
	MergeMethod   string   `json:"merge_method"`
	Reviewers     []string `json:"reviewers"`
	TeamReviewers []string `json:"team_reviewers"`
}

// BuildRequest maps a tool name onto its request builder. The switch is
// exhaustive over the manifest's tools table; an unknown name is a typed
// refusal naming it, never a silent no-op.
func BuildRequest(tool string, a Args) (Request, error) {
	switch tool {
	case "repos.list":
		return ListReposRequest(a.Owner, a.PerPage)
	case "repos.get", "repos.clone_url":
		return GetRepoRequest(a.Owner, a.Repo)
	case "issues.list":
		return ListIssuesRequest(a.Owner, a.Repo, a.State, a.PerPage)
	case "issues.get":
		return GetIssueRequest(a.Owner, a.Repo, a.Number)
	case "issues.create":
		return CreateIssueRequest(a.Owner, a.Repo, a.Title, a.Body, a.Labels)
	case "issues.comment":
		return CommentIssueRequest(a.Owner, a.Repo, a.Number, a.Body)
	case "issues.close":
		return CloseIssueRequest(a.Owner, a.Repo, a.Number)
	case "prs.list":
		return ListPRsRequest(a.Owner, a.Repo, a.State, a.PerPage)
	case "prs.get":
		return GetPRRequest(a.Owner, a.Repo, a.Number)
	case "prs.create":
		return CreatePRRequest(a.Owner, a.Repo, a.Title, a.Head, a.Base, a.Body, a.Draft)
	case "prs.merge":
		return MergePRRequest(a.Owner, a.Repo, a.Number, a.MergeMethod)
	case "prs.review_request":
		return ReviewRequestRequest(a.Owner, a.Repo, a.Number, a.Reviewers, a.TeamReviewers)
	default:
		return Request{}, cascade.Newf(cascade.KindNotFound,
			"cascade-github: no such tool %q", ToolNamespace+"."+tool)
	}
}
