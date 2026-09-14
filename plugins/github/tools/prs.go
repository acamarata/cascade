package tools

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the pull-request tool group — list, get, create, merge,
//
//	review_request.
//
// Inputs: tool arguments; raw GitHub REST bytes.
// Outputs: decoded pull requests, or a typed error.
// Constraints: see repos.go's package doc.
// SPORT: plugins/github tools/prs (ADD) — P1-E25-W5-S51-T1.

// PullRequest is the subset of a GitHub pull request this plugin exposes.
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
	User   User   `json:"user"`
	// Draft reports whether the PR is still a draft. A draft that merged
	// silently would be a real incident, so it is carried explicitly.
	Draft bool `json:"draft"`
	// Merged is present on a single-PR response; the LIST endpoint omits
	// it, which is why MergedAt is carried too. See IsMerged.
	Merged   bool   `json:"merged"`
	MergedAt string `json:"merged_at"`
	Head     PRRef  `json:"head"`
	Base     PRRef  `json:"base"`
	HTMLURL  string `json:"html_url"`
}

// PRRef is one end of a pull request: its branch and the repo it lives in.
type PRRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *Repo  `json:"repo"`
}

// IsMerged reports whether this pull request has been merged.
//
// It reads BOTH fields on purpose. GitHub's single-PR response carries a
// boolean `merged`, but the LIST response does not — it carries only
// `merged_at`, leaving the boolean at its zero value. A check written
// against `Merged` alone therefore reports every merged PR in a list as
// unmerged, which the captured list fixture makes visible.
func (p PullRequest) IsMerged() bool { return p.Merged || p.MergedAt != "" }

// DecodePullRequest decodes a single pull-request response.
func DecodePullRequest(body []byte) (PullRequest, error) {
	var out PullRequest
	if err := json.Unmarshal(body, &out); err != nil {
		return PullRequest{}, cascade.Wrap(cascade.KindIntegrity, err, "github: decoding a pull-request response")
	}
	return out, nil
}

// DecodePullRequests decodes a pull-request list response.
func DecodePullRequests(body []byte) ([]PullRequest, error) {
	var out []PullRequest
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "github: decoding a pull-request list response")
	}
	return out, nil
}

// MergeResult is GitHub's answer to a merge.
//
// Merged is authoritative here, unlike on a listed pull request: this is
// the merge endpoint's own response, and it says outright whether the merge
// happened. A merge that GitHub declines (the branch moved, checks are
// required) comes back as an error status, not as Merged=false, so a false
// here with no error means the call did not do what it was asked.
type MergeResult struct {
	SHA     string `json:"sha"`
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

// DecodeMergeResult decodes a merge response.
func DecodeMergeResult(body []byte) (MergeResult, error) {
	var out MergeResult
	if err := json.Unmarshal(body, &out); err != nil {
		return MergeResult{}, cascade.Wrap(cascade.KindIntegrity, err,
			"github: decoding a merge response")
	}
	return out, nil
}

// ListPRsRequest builds the request listing a repository's pull requests.
func ListPRsRequest(owner, repo, state string, perPage int) (Request, error) {
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
	path := repoPath(owner, repo) + "/pulls"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return Request{Method: "GET", Path: path}, nil
}

// GetPRRequest builds the request fetching one pull request.
func GetPRRequest(owner, repo string, number int) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if err := validNumber(number); err != nil {
		return Request{}, err
	}
	return Request{Method: "GET", Path: repoPath(owner, repo) + "/pulls/" + strconv.Itoa(number)}, nil
}

// CreatePRRequest builds the request opening a pull request. head and base
// are both required: GitHub defaults neither, and a PR opened against the
// wrong base is an expensive mistake to undo.
func CreatePRRequest(owner, repo, title, head, base, body string, draft bool) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	for field, value := range map[string]string{"title": title, "head": head, "base": base} {
		if strings.TrimSpace(value) == "" {
			return Request{}, cascade.Newf(cascade.KindInvalidInput, "github: a pull request needs a %s", field)
		}
	}
	if head == base {
		return Request{}, cascade.Newf(cascade.KindInvalidInput,
			"github: head and base are both %q; a pull request cannot merge a branch into itself", head)
	}
	payload := map[string]any{"title": title, "head": head, "base": base, "draft": draft}
	if body != "" {
		payload["body"] = body
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Request{}, cascade.Wrap(cascade.KindInternal, err, "github: encoding a pull request")
	}
	return Request{Method: "POST", Path: repoPath(owner, repo) + "/pulls", Body: encoded}, nil
}

// MergeMethods are the three merge strategies GitHub accepts.
var MergeMethods = []string{"merge", "squash", "rebase"}

// MergePRRequest builds the request merging a pull request. An unrecognized
// method is refused locally: GitHub would reject it too, but only after the
// call, and a merge is not a call to make speculatively.
func MergePRRequest(owner, repo string, number int, method string) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if err := validNumber(number); err != nil {
		return Request{}, err
	}
	if !validMergeMethod(method) {
		return Request{}, cascade.Newf(cascade.KindInvalidInput,
			"github: merge method %q is not one of %s", method, strings.Join(MergeMethods, ", "))
	}
	encoded, err := json.Marshal(map[string]string{"merge_method": method})
	if err != nil {
		return Request{}, cascade.Wrap(cascade.KindInternal, err, "github: encoding a merge")
	}
	path := repoPath(owner, repo) + "/pulls/" + strconv.Itoa(number) + "/merge"
	return Request{Method: "PUT", Path: path, Body: encoded}, nil
}

// ReviewRequestRequest builds the request asking for reviews. At least one
// reviewer is required: an empty request would succeed at the API and
// change nothing, which reads to the caller as a review that was requested.
func ReviewRequestRequest(owner, repo string, number int, reviewers, teamReviewers []string) (Request, error) {
	if err := validSegments(owner, repo); err != nil {
		return Request{}, err
	}
	if err := validNumber(number); err != nil {
		return Request{}, err
	}
	if len(reviewers) == 0 && len(teamReviewers) == 0 {
		return Request{}, cascade.New(cascade.KindInvalidInput,
			"github: a review request needs at least one reviewer or team")
	}
	payload := map[string]any{}
	if len(reviewers) > 0 {
		payload["reviewers"] = reviewers
	}
	if len(teamReviewers) > 0 {
		payload["team_reviewers"] = teamReviewers
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Request{}, cascade.Wrap(cascade.KindInternal, err, "github: encoding a review request")
	}
	path := repoPath(owner, repo) + "/pulls/" + strconv.Itoa(number) + "/requested_reviewers"
	return Request{Method: "POST", Path: path, Body: encoded}, nil
}

// validMergeMethod reports whether method is one GitHub accepts.
func validMergeMethod(method string) bool {
	for _, m := range MergeMethods {
		if method == m {
			return true
		}
	}
	return false
}
