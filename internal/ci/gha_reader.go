// Purpose (this file): the two GitHubReader reads the resolver's hosted
// guard rests on (P1-CI-03). *Client implements GitHubReader over the same
// ci-poll egress gate and never-pay route guard PollRuns uses.
//
// Inputs: the GitHub REST API.
// Outputs: repository visibility and commit presence.
// Constraints: fail closed: anything but an explicit public answer is
// private, and only 200 naming the same sha is present. Both reads take the
// ci-poll capability before any byte leaves the process; a repository
// [ci.policy] routes local is refused by the route guard first. GET only.
// SPORT: internal.ci.Client.RepoVisibility/ADDED,
//
//	internal.ci.Client.CommitExists/ADDED (P1-CI-03).

package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/acamarata/cascade/pkg/cascade"
)

// githubGet runs one GET through the route guard and the ci-poll gate.
func (c *Client) githubGet(ctx context.Context, owner, repo, path string) (HTTPResponse, error) {
	if !githubName.MatchString(owner) || !githubName.MatchString(repo) {
		return HTTPResponse{}, cascade.Newf(cascade.KindInvalidInput, "ci: invalid GitHub repository %q/%q", owner, repo)
	}
	if err := guardActionsPoll(ctx, c.routes, owner+"/"+repo); err != nil {
		return HTTPResponse{}, err
	}
	if _, err := c.acquireCIPollGate(); err != nil {
		return HTTPResponse{}, err
	}
	url := fmt.Sprintf("%s/repos/%s/%s%s", c.baseURL, owner, repo, path)
	resp, err := c.doer.Do(ctx, HTTPRequest{Method: "GET", URL: url, Headers: map[string]string{"Accept": "application/vnd.github+json"}})
	if err != nil {
		return HTTPResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: the outbound request failed")
	}
	return resp, nil
}

var githubName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// RepoVisibility reports whether owner/repo is private. Anything but a 200
// whose body says private false and visibility public is private.
func (c *Client) RepoVisibility(ctx context.Context, owner, repo string) (bool, error) {
	resp, err := c.githubGet(ctx, owner, repo, "")
	if err != nil {
		return true, err
	}
	if resp.Status != 200 {
		return true, cascade.Newf(cascade.KindUnavailable, "ci: repository visibility returned HTTP %d", resp.Status)
	}
	var body struct {
		Private    *bool  `json:"private"`
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || body.Private == nil {
		return true, cascade.New(cascade.KindIntegrity, "ci: repository visibility response has no private field")
	}
	return *body.Private || body.Visibility != "public", nil
}

// CommitExists reports whether sha is on owner/repo: 200 with the same sha
// is true, 404 and 422 are false, anything else is an error.
func (c *Client) CommitExists(ctx context.Context, owner, repo, sha string) (bool, error) {
	if !commitSHA.MatchString(sha) {
		return false, cascade.Newf(cascade.KindInvalidInput, "ci: %q is not a full lowercase commit id", sha)
	}
	resp, err := c.githubGet(ctx, owner, repo, "/commits/"+sha)
	if err != nil {
		return false, err
	}
	switch resp.Status {
	case 200:
		var body struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil || body.SHA != sha {
			return false, cascade.Newf(cascade.KindIntegrity, "ci: commit response does not name %s", sha)
		}
		return true, nil
	case 404, 422:
		return false, nil
	}
	return false, cascade.Newf(cascade.KindUnavailable, "ci: commit lookup returned HTTP %d", resp.Status)
}
