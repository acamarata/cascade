// Purpose: poll.go pagination and transport-error tests. Split from
//
//	poll_test.go per the repo's 300-line file cap (mechanical relocation,
//	no behavior change, matching domains_layout_test.go's own split
//	precedent) -- shared test doubles (fakeDoer, errDoer, testEngine,
//	buildEngine, nopVault) live in poll_test.go.
//
// SPORT: internal.ci.Client.PollRuns/TESTED (P1-E25-W5-S51-T2).
package ci

import (
	"context"
	"fmt"
	"testing"
)

// runList builds n synthetic run objects starting at id startID, used
// only to exercise this package's own pagination-loop arithmetic -- the
// wire SHAPE itself is covered by normalize_test.go's real fixtures.
func runList(n, startID int) string {
	out := "["
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(`{"id":%d,"status":"completed","conclusion":"success"}`, startID+i)
	}
	return out + "]"
}

// TestPollRuns_Pagination asserts a total_count larger than one page
// fetches a second page and accumulates both.
func TestPollRuns_Pagination(t *testing.T) {
	page1 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1"
	page2 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=2"
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		page1: {Status: 200, Body: []byte(fmt.Sprintf(`{"total_count":150,"workflow_runs":%s}`, runList(100, 1)))},
		page2: {Status: 200, Body: []byte(fmt.Sprintf(`{"total_count":150,"workflow_runs":%s}`, runList(50, 101)))},
	}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	res, err := c.PollRuns(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("PollRuns: %v", err)
	}
	if len(res.Runs) != 150 {
		t.Fatalf("got %d runs across pages, want 150", len(res.Runs))
	}
}

// TestPollRuns_NonOKStatus asserts a non-200/304 status is a refusal, not
// a silently-empty result.
func TestPollRuns_NonOKStatus(t *testing.T) {
	url := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1"
	doer := &fakeDoer{responses: map[string]HTTPResponse{url: {Status: 500}}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected an error for HTTP 500")
	}
}

// TestPollJobs_NotModifiedAndErrors covers PollJobs's 304, non-200 and
// malformed-body branches.
func TestPollJobs_NotModifiedAndErrors(t *testing.T) {
	url304 := "https://api.github.com/repos/o/r/actions/runs/1/jobs?per_page=100"
	doer := &fakeDoer{responses: map[string]HTTPResponse{url304: {Status: 304}}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	res, err := c.PollJobs(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatalf("PollJobs (304): %v", err)
	}
	if !res.NotModified {
		t.Error("expected NotModified on a 304")
	}

	url500 := "https://api.github.com/repos/o/r/actions/runs/2/jobs?per_page=100"
	doer2 := &fakeDoer{responses: map[string]HTTPResponse{url500: {Status: 500}}}
	c2 := NewClient(doer2, testEngine(t), "https://api.github.com", 1)
	if _, err := c2.PollJobs(context.Background(), "o", "r", 2); err == nil {
		t.Error("expected an error for HTTP 500")
	}

	urlBad := "https://api.github.com/repos/o/r/actions/runs/3/jobs?per_page=100"
	doer3 := &fakeDoer{responses: map[string]HTTPResponse{urlBad: {Status: 200, Body: []byte(`not json`)}}}
	c3 := NewClient(doer3, testEngine(t), "https://api.github.com", 1)
	if _, err := c3.PollJobs(context.Background(), "o", "r", 3); err == nil {
		t.Error("expected an error for malformed job JSON")
	}
}

func TestPollRuns_TransportError(t *testing.T) {
	c := NewClient(errDoer{err: context.DeadlineExceeded}, testEngine(t), "https://api.github.com", 1)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected an error when the transport fails")
	}
}

type twoPageDoer struct {
	page1URL  string
	page1Body []byte
}

func (d *twoPageDoer) Do(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
	if req.URL == d.page1URL {
		return HTTPResponse{Status: 200, Body: d.page1Body}, nil
	}
	return HTTPResponse{}, context.DeadlineExceeded
}

// TestPollRuns_SecondPageTransportError covers the pagination loop's own
// error branch (distinct from the first page's doGet error path).
func TestPollRuns_SecondPageTransportError(t *testing.T) {
	page1 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1"
	doer := &twoPageDoer{page1URL: page1, page1Body: []byte(fmt.Sprintf(`{"total_count":150,"workflow_runs":%s}`, runList(100, 1)))}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected an error when the second page's transport fails")
	}
}

// TestPollRuns_SecondPageNonOK covers the pagination loop's non-200
// status branch.
func TestPollRuns_SecondPageNonOK(t *testing.T) {
	page1 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1"
	page2 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=2"
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		page1: {Status: 200, Body: []byte(fmt.Sprintf(`{"total_count":150,"workflow_runs":%s}`, runList(100, 1)))},
		page2: {Status: 500},
	}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected an error when a later page returns HTTP 500")
	}
}

// TestPollRuns_SecondPageMalformedBody covers the pagination loop's
// decode-error branch.
func TestPollRuns_SecondPageMalformedBody(t *testing.T) {
	page1 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1"
	page2 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=2"
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		page1: {Status: 200, Body: []byte(fmt.Sprintf(`{"total_count":150,"workflow_runs":%s}`, runList(100, 1)))},
		page2: {Status: 200, Body: []byte(`not json`)},
	}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected an error when a later page's body is malformed")
	}
}

// TestPollRuns_SecondPageEmpty covers the "a later page returns zero
// items" early-break branch, when total_count over-promised.
func TestPollRuns_SecondPageEmpty(t *testing.T) {
	page1 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1"
	page2 := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=2"
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		page1: {Status: 200, Body: []byte(fmt.Sprintf(`{"total_count":150,"workflow_runs":%s}`, runList(100, 1)))},
		page2: {Status: 200, Body: []byte(`{"total_count":150,"workflow_runs":[]}`)},
	}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	res, err := c.PollRuns(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("PollRuns: %v", err)
	}
	if len(res.Runs) != 100 {
		t.Errorf("got %d runs, want 100 (the second page was empty and should stop the loop)", len(res.Runs))
	}
}

// TestPollRuns_MalformedFirstPage covers PollRuns's first-page
// decode-error branch.
func TestPollRuns_MalformedFirstPage(t *testing.T) {
	url := "https://api.github.com/repos/o/r/actions/runs?per_page=100&page=1"
	doer := &fakeDoer{responses: map[string]HTTPResponse{url: {Status: 200, Body: []byte(`not json`)}}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected an error for a malformed first page")
	}
}
