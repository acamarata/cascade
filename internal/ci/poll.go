// Purpose: the GitHub Actions REST polling client (P1-E25-W5-S51-T2, task
//
//	2): list workflow runs and their jobs, with ETag conditional requests
//	and page-based pagination, gated by the ci-poll egress class.
//
// Inputs: a Doer (production: a *http.Client adapter built by the
//
//	composition root, matching internal/providers/intake/transport.go's
//	own "Doer interface here, concrete net/http adapter in the
//	composition root" split -- this package never imports net/http, so it
//	never needs an internal/build egress-allowlist entry of its own) and
//	an *egress.Engine.
//
// Outputs: PollResult{Runs, Jobs, Steps, NotModified} on success, or a
//
//	typed cascade.Error. On HTTP 304 Not Modified, NotModified is true and
//	Runs/Jobs/Steps are nil -- the caller must skip Upsert entirely, never
//	write an empty page over real data (R-21.265's own "a call made with a
//	zero Capability or against a disabled class returns before any byte
//	is read" framing applies equally here: a 304 short-circuits before any
//	normalize call, not merely before any write).
//
// Constraints: every outbound call transits egress.Engine.Capability +
//
//	egress.SensitivityPass on EgressClassCIPoll before doer.Do runs, per
//	06 §5.17 (matching intake.acquireIntakeGate's exact two-check
//	sequence). Pagination stops once the accumulated run/job count
//	reaches the page's own total_count, or once a page returns fewer
//	than PerPage items, whichever comes first -- never an unbounded loop.
//
// SPORT: internal.ci.Client/ADDED, internal.ci.Client.PollRuns/ADDED
//
//	(P1-E25-W5-S51-T2).

package ci

import (
	"context"
	"fmt"

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
	doer    Doer
	engine  *egress.Engine
	baseURL string
	repoID  int64
	etags   map[string]string
}

// NewClient builds a Client. baseURL defaults to https://api.github.com
// when empty.
func NewClient(doer Doer, engine *egress.Engine, baseURL string, repoID int64) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{doer: doer, engine: engine, baseURL: baseURL, repoID: repoID, etags: map[string]string{}}
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

// PollRuns fetches every workflow-run page for owner/repo (pagination by
// page number, stopping once the accumulated count reaches the first
// page's own total_count or a page returns fewer than perPage items),
// applying the If-None-Match conditional header -- cached from a prior
// call -- to the FIRST page's request only. A 304 on that first page
// short-circuits the whole call with PollResult.NotModified=true and no
// normalize call; GitHub's own semantics make a conditional 304 on page 1
// mean "nothing changed," so later pages are never fetched in that case.
func (c *Client) PollRuns(ctx context.Context, owner, repo string) (PollResult, error) {
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
