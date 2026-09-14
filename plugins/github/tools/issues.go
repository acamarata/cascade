package tools

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the issues tool group — list, get, create, comment, close.
// Inputs: tool arguments; raw GitHub REST bytes.
// Outputs: decoded issues, or a typed error.
// Constraints: see repos.go's package doc. A pull request is also an issue
//
//	in GitHub's data model, and List deliberately keeps that visible rather
//	than filtering silently — see Issue.IsPullRequest.
//
// SPORT: plugins/github tools/issues (ADD) — P1-E25-W5-S51-T1.

// Issue is the subset of a GitHub issue this plugin exposes.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
	User   User   `json:"user"`
	// HTMLURL is the browser URL, the one a human is given.
	HTMLURL string `json:"html_url"`
	// Labels carries each label's name.
	Labels []Label `json:"labels"`
	// PullRequest is present ONLY when this issue is really a pull
	// request. GitHub returns PRs from the issues endpoint, and a caller
	// that did not know would double-count them.
	PullRequest *IssuePullRef `json:"pull_request,omitempty"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
}

// Label is one issue label.
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// IssuePullRef is the marker GitHub attaches to an issue that is a pull
// request.
type IssuePullRef struct {
	HTMLURL string `json:"html_url"`
}

// IsPullRequest reports whether this issue is really a pull request.
//
// GitHub's /issues endpoint returns both, distinguished only by the
// presence of the pull_request member. A tool that reported PRs as issues
// would make every issue count wrong, so the distinction is surfaced rather
// than hidden — the captured fixture contains both kinds, which is how this
// is tested against reality rather than against an assumption.
func (i Issue) IsPullRequest() bool { return i.PullRequest != nil }

// DecodeIssue decodes a single issue response.
func DecodeIssue(body []byte) (Issue, error) {
	var out Issue
	if err := json.Unmarshal(body, &out); err != nil {
		return Issue{}, cascade.Wrap(cascade.KindIntegrity, err, "github: decoding an issue response")
	}
	return out, nil
}

// DecodeIssues decodes an issue list response.
func DecodeIssues(body []byte) ([]Issue, error) {
	var out []Issue
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "github: decoding an issue list response")
	}
	return out, nil
}

// IssueComment is the record GitHub returns after a comment is posted.
// The browser URL is the useful half: it is what a human is handed to see
// the comment that was just made.
type IssueComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	User    User   `json:"user"`
	HTMLURL string `json:"html_url"`
}

// DecodeIssueComment decodes a comment-creation response.
func DecodeIssueComment(body []byte) (IssueComment, error) {
	var out IssueComment
	if err := json.Unmarshal(body, &out); err != nil {
		return IssueComment{}, cascade.Wrap(cascade.KindIntegrity, err,
			"github: decoding an issue comment response")
	}
	return out, nil
}

// ListIssuesRequest builds the request listing a repository's issues.
// state must be one of open, closed, all; an unrecognized state is refused
// rather than silently sent for GitHub to reject.
func ListIssuesRequest(owner, repo, state string, perPage int) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if err := validIssueState(state); err != nil {
		return Request{}, err
	}
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if perPage > 0 {
		q.Set("per_page", strconv.Itoa(perPage))
	}
	path := repoPath(owner, repo) + "/issues"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return Request{Method: "GET", Path: path}, nil
}

// GetIssueRequest builds the request fetching one issue.
func GetIssueRequest(owner, repo string, number int) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if err := validNumber(number); err != nil {
		return Request{}, err
	}
	return Request{Method: "GET", Path: repoPath(owner, repo) + "/issues/" + strconv.Itoa(number)}, nil
}

// CreateIssueRequest builds the request opening an issue. An empty title is
// refused here rather than at the API: a 422 round trip tells the operator
// less than this does.
func CreateIssueRequest(owner, repo, title, body string, labels []string) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if strings.TrimSpace(title) == "" {
		return Request{}, cascade.New(cascade.KindInvalidInput, "github: an issue needs a title")
	}
	payload := map[string]any{"title": title}
	if body != "" {
		payload["body"] = body
	}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Request{}, cascade.Wrap(cascade.KindInternal, err, "github: encoding an issue")
	}
	return Request{Method: "POST", Path: repoPath(owner, repo) + "/issues", Body: encoded}, nil
}

// CommentIssueRequest builds the request adding a comment.
func CommentIssueRequest(owner, repo string, number int, comment string) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if err := validNumber(number); err != nil {
		return Request{}, err
	}
	if strings.TrimSpace(comment) == "" {
		return Request{}, cascade.New(cascade.KindInvalidInput, "github: a comment needs a body")
	}
	encoded, err := json.Marshal(map[string]string{"body": comment})
	if err != nil {
		return Request{}, cascade.Wrap(cascade.KindInternal, err, "github: encoding a comment")
	}
	path := repoPath(owner, repo) + "/issues/" + strconv.Itoa(number) + "/comments"
	return Request{Method: "POST", Path: path, Body: encoded}, nil
}

// CloseIssueRequest builds the request closing an issue. It PATCHes state
// only, so it can never overwrite a title or body a concurrent editor
// changed between read and write.
func CloseIssueRequest(owner, repo string, number int) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if err := validNumber(number); err != nil {
		return Request{}, err
	}
	encoded, err := json.Marshal(map[string]string{"state": "closed"})
	if err != nil {
		return Request{}, cascade.Wrap(cascade.KindInternal, err, "github: encoding a close")
	}
	path := repoPath(owner, repo) + "/issues/" + strconv.Itoa(number)
	return Request{Method: "PATCH", Path: path, Body: encoded}, nil
}

// validIssueState refuses a state GitHub does not define. An empty state is
// allowed and means "use the API's own default".
func validIssueState(state string) error {
	switch state {
	case "", "open", "closed", "all":
		return nil
	default:
		return cascade.Newf(cascade.KindInvalidInput,
			"github: issue state %q is not one of open, closed, all", state)
	}
}

// validNumber refuses a non-positive issue or PR number.
func validNumber(number int) error {
	if number <= 0 {
		return cascade.Newf(cascade.KindInvalidInput, "github: number %d is not a positive issue or pull-request number", number)
	}
	return nil
}
