// Purpose: GitHub Actions polling, dispatch and artifact transport.
// Inputs: injected transport, egress engine and never-pay route resolver.
// Outputs: runs, jobs and artifact archives, or typed failures.
// Constraints: route and ci-poll gates precede every outbound operation.
// A 304 returns no rows; callers must skip writes. Dispatch additionally
// requires admitted hosted configuration and live public visibility.
// SPORT: internal.ci.Client (P1-E25-W5-S51-T2, P1-NODE-09).

package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// perPage is the fixed page size this client requests. GitHub's own
// ceiling for this endpoint is 100.
const perPage = 100

// Doer performs one outbound HTTP call in this package's own vocabulary --
// declared locally so a recording fake never needs "net"/"net/http" to
// satisfy it, matching every landed driver's identical pattern.
type Doer interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// HTTPRequest is one outbound request.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is one inbound response, fully buffered.
type HTTPResponse struct {
	Status int
	Header map[string]string
	Body   []byte
}

// PollResult is one PollRuns/PollJobs call's outcome.
type PollResult struct {
	Runs        []Run
	Jobs        []Job
	Steps       []Step
	NotModified bool
	ETag        string
}

// Client is the GitHub Actions polling client for one (owner, repo).
type Client struct {
	// Allowed reads the admitted ci.hosted.allow value; nil refuses dispatch.
	Allowed func() (bool, error)
	doer    Doer
	engine  *egress.Engine
	routes  RouteResolver
	baseURL string
	repoID  int64
	etags   map[string]string
}

// Artifact identifies one run artifact returned by GitHub.
type Artifact struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Expired bool   `json:"expired"`
}

// TriggerWorkflowDispatch starts a workflow only after explicit admission.
// The hosted runner must also enforce the job sensitivity and source guards.
func (c *Client) TriggerWorkflowDispatch(ctx context.Context, owner, repo, workflow, ref string) error {
	if c.Allowed == nil {
		return cascade.New(cascade.KindPolicyDenied, "ci: ci.hosted.allow is not admitted")
	}
	allowed, err := c.Allowed()
	if err != nil {
		return err
	}
	if !allowed {
		return cascade.New(cascade.KindPolicyDenied, "ci: ci.hosted.allow is false")
	}
	if !githubName.MatchString(workflow) || !strings.HasSuffix(workflow, ".yml") || ref == "" {
		return cascade.New(cascade.KindInvalidInput, "ci: invalid workflow filename or ref")
	}
	private, err := c.RepoVisibility(ctx, owner, repo)
	if err != nil {
		return err
	}
	if private {
		return cascade.New(cascade.KindPolicyDenied, "ci: hosted dispatch requires a public repository")
	}
	if err := guardActionsPoll(ctx, c.routes, owner+"/"+repo); err != nil {
		return err
	}
	if _, err := c.acquireCIPollGate(); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"ref": ref})
	url := fmt.Sprintf("%s/repos/%s/%s/actions/workflows/%s/dispatches", c.baseURL, owner, repo, workflow)
	resp, err := c.doer.Do(ctx, HTTPRequest{Method: "POST", URL: url, Body: body,
		Headers: map[string]string{"Accept": "application/vnd.github+json", "Content-Type": "application/json"}})
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: dispatch request failed")
	}
	if resp.Status != 204 {
		return cascade.Newf(cascade.KindUnavailable, "ci: dispatch returned HTTP %d", resp.Status)
	}
	return nil
}

// ListRunArtifacts lists every artifact page for the correlated run.
func (c *Client) ListRunArtifacts(ctx context.Context, owner, repo string, runID int64) ([]Artifact, error) {
	if runID <= 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "ci: invalid run id")
	}
	var artifacts []Artifact
	for page := 1; ; page++ {
		path := fmt.Sprintf("/actions/runs/%d/artifacts?per_page=%d&page=%d", runID, perPage, page)
		resp, err := c.githubGet(ctx, owner, repo, path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			return nil, cascade.Newf(cascade.KindUnavailable, "ci: list artifacts returned HTTP %d", resp.Status)
		}
		var body struct {
			Total     int        `json:"total_count"`
			Artifacts []Artifact `json:"artifacts"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "ci: invalid artifacts response")
		}
		artifacts = append(artifacts, body.Artifacts...)
		if len(body.Artifacts) < perPage || len(artifacts) >= body.Total {
			return artifacts, nil
		}
	}
}

// DownloadArtifact fetches the ZIP bytes; its contents are not trusted evidence.
// The transport owns GitHub's archive redirect and must not forward credentials.
func (c *Client) DownloadArtifact(ctx context.Context, owner, repo string, artifactID int64) ([]byte, error) {
	if artifactID <= 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "ci: invalid artifact id")
	}
	resp, err := c.githubGet(ctx, owner, repo, fmt.Sprintf("/actions/artifacts/%d/zip", artifactID))
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, cascade.Newf(cascade.KindUnavailable, "ci: download artifact returned HTTP %d", resp.Status)
	}
	return resp.Body, nil
}

// NewClient builds a Client with a default baseURL of https://api.github.com.
// Every verb refuses a nil never-pay route resolver.
func NewClient(doer Doer, engine *egress.Engine, baseURL string, repoID int64, routes RouteResolver) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{doer: doer, engine: engine, routes: routes, baseURL: baseURL, repoID: repoID, etags: map[string]string{}}
}

// acquireCIPollGate runs the two checks R-21.265/06-§5.17 require before
// any byte leaves the process on the ci-poll class.
func (c *Client) acquireCIPollGate() (egress.Capability, error) {
	token, err := c.engine.Capability(egress.EgressClassCIPoll)
	if err != nil {
		return egress.Capability{}, cascade.Wrap(cascade.KindPermissionDenied, err, "ci: ci-poll egress class refused")
	}
	cfg, ok := c.engine.Registry().Lookup(egress.EgressClassCIPoll)
	if !ok {
		return egress.Capability{}, cascade.New(cascade.KindPermissionDenied, "ci: ci-poll egress class is not registered")
	}
	if serr := egress.SensitivityPass(egress.EgressClassCIPoll, cfg, egress.TierInternal); serr != nil {
		return egress.Capability{}, cascade.Wrap(cascade.KindPermissionDenied, serr, "ci: ci-poll sensitivity pass refused")
	}
	return token, nil
}

// PollRuns fetches run pages up to total_count. Only the first page carries
// a cached ETag; its 304 returns NotModified without fetching later pages.
func (c *Client) PollRuns(ctx context.Context, owner, repo string) (PollResult, error) {
	if err := guardActionsPoll(ctx, c.routes, owner+"/"+repo); err != nil {
		return PollResult{}, err
	}
	if _, err := c.acquireCIPollGate(); err != nil {
		return PollResult{}, err
	}
	base := fmt.Sprintf("%s/repos/%s/%s/actions/runs", c.baseURL, owner, repo)
	firstURL := fmt.Sprintf("%s?per_page=%d&page=1", base, perPage)
	resp, err := c.doGet(ctx, firstURL)
	if err != nil {
		return PollResult{}, err
	}
	if resp.Status == 304 {
		return PollResult{NotModified: true}, nil
	}
	if resp.Status != 200 {
		return PollResult{}, cascade.Newf(cascade.KindUnavailable, "ci: list runs returned HTTP %d", resp.Status)
	}
	runs, total, err := normalizeActionsRunsPage(resp.Body, c.repoID)
	if err != nil {
		return PollResult{}, err
	}
	c.rememberETag(firstURL, resp.Header)
	etag := resp.Header["Etag"]

	for page := 2; len(runs) < total && page <= pageCount(total, perPage); page++ {
		pageURL := fmt.Sprintf("%s?per_page=%d&page=%d", base, perPage, page)
		pageResp, perr := c.doer.Do(ctx, HTTPRequest{Method: "GET", URL: pageURL, Headers: map[string]string{"Accept": "application/vnd.github+json"}})
		if perr != nil {
			return PollResult{}, cascade.Wrap(cascade.KindUnavailable, perr, "ci: the outbound request failed")
		}
		if pageResp.Status != 200 {
			return PollResult{}, cascade.Newf(cascade.KindUnavailable, "ci: list runs page %d returned HTTP %d", page, pageResp.Status)
		}
		more, _, derr := normalizeActionsRunsPage(pageResp.Body, c.repoID)
		if derr != nil {
			return PollResult{}, derr
		}
		if len(more) == 0 {
			break
		}
		runs = append(runs, more...)
	}
	return PollResult{Runs: runs, ETag: etag}, nil
}

// PollJobs fetches every job (and its steps) for one run.
func (c *Client) PollJobs(ctx context.Context, owner, repo string, runID int64) (PollResult, error) {
	if err := guardActionsPoll(ctx, c.routes, owner+"/"+repo); err != nil {
		return PollResult{}, err
	}
	if _, err := c.acquireCIPollGate(); err != nil {
		return PollResult{}, err
	}
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/jobs?per_page=%d", c.baseURL, owner, repo, runID, perPage)
	resp, err := c.doGet(ctx, url)
	if err != nil {
		return PollResult{}, err
	}
	if resp.Status == 304 {
		return PollResult{NotModified: true}, nil
	}
	if resp.Status != 200 {
		return PollResult{}, cascade.Newf(cascade.KindUnavailable, "ci: list jobs returned HTTP %d", resp.Status)
	}
	jobs, steps, err := normalizeActionsJobs(resp.Body)
	if err != nil {
		return PollResult{}, err
	}
	c.rememberETag(url, resp.Header)
	return PollResult{Jobs: jobs, Steps: steps, ETag: resp.Header["Etag"]}, nil
}

// doGet issues one conditional GET, attaching If-None-Match when a prior
// ETag is cached for url.
func (c *Client) doGet(ctx context.Context, url string) (HTTPResponse, error) {
	headers := map[string]string{"Accept": "application/vnd.github+json"}
	if etag, ok := c.etags[url]; ok {
		headers["If-None-Match"] = etag
	}
	resp, err := c.doer.Do(ctx, HTTPRequest{Method: "GET", URL: url, Headers: headers})
	if err != nil {
		return HTTPResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: the outbound request failed")
	}
	return resp, nil
}

// rememberETag caches header's Etag value for url, so the NEXT call to the
// same URL sends it back as If-None-Match.
func (c *Client) rememberETag(url string, header map[string]string) {
	if etag, ok := header["Etag"]; ok && etag != "" {
		c.etags[url] = etag
	}
}

// pageCount computes how many pages a total_count implies at perPage items
// per page, rounding up -- PollRuns's loop bound, kept as one tested rule
// rather than inlined.
func pageCount(total, perPage int) int {
	if perPage <= 0 {
		return 0
	}
	n := total / perPage
	if total%perPage != 0 {
		n++
	}
	return n
}
