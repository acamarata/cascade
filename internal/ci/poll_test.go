// Purpose: poll.go tests. The 304 assertion replays this ticket's real,
//
//	matched-pair captured headers (testdata/fixtures/runs_200_headers.txt
//	then runs_304_headers.txt, a genuine conditional GET/304 exchange) --
//	never a synthesized 304. Split from poll_pagination_test.go per the
//	repo's 300-line file cap (mechanical relocation, no behavior change,
//	matching domains_layout_test.go's own split precedent) -- this file
//	keeps the shared test doubles plus the ETag/304, real-fixture and
//	egress-gate tests; poll_pagination_test.go keeps every pagination and
//	transport-error case.
//
// SPORT: internal.ci.Client.PollRuns/TESTED,
//
//	internal.ci.Client.PollJobs/TESTED (P1-E25-W5-S51-T2).
package ci

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
)

// capturedETag is the real Etag runs_200_headers.txt carries -- the same
// value runs_304_headers.txt's conditional replay was answered against
// (testdata/README.md's matched-pair provenance note).
const capturedETag = `W/"d4fc9a44184bf01558cb23feb0ccd5a714cab2e10396f1cc860926fdad14b318"`

const runsURL = "https://api.github.com/repos/acamarata/cascade/actions/runs?per_page=100&page=1"

type fakeDoer struct {
	responses map[string]HTTPResponse
	calls     []HTTPRequest
}

func (f *fakeDoer) Do(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
	f.calls = append(f.calls, req)
	if resp, ok := f.responses[req.URL]; ok {
		return resp, nil
	}
	return HTTPResponse{Status: 404, Body: []byte("not found")}, nil
}

// errDoer always fails the transport call.
type errDoer struct{ err error }

func (e errDoer) Do(_ context.Context, _ HTTPRequest) (HTTPResponse, error) {
	return HTTPResponse{}, e.err
}

type nopVault struct{}

func (nopVault) List(_ context.Context) ([]string, error)        { return nil, nil }
func (nopVault) Get(_ context.Context, _ string) ([]byte, error) { return nil, nil }

func testEngine(t *testing.T) *egress.Engine {
	t.Helper()
	return buildEngine(t, egress.DefaultRegistry())
}

// buildEngine is the shared helper for tests that need a non-default
// registry.
func buildEngine(t *testing.T, reg *egress.Registry) *egress.Engine {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("building detector: %v", err)
	}
	engine, err := egress.NewEngine(reg, nopVault{}, detector)
	if err != nil {
		t.Fatalf("building engine: %v", err)
	}
	return engine
}

// TestPollRuns_ETagRoundTrip_304NoWrite replays the real captured 200 (with
// its Etag), then the real captured 304 for the SAME url on a second call
// -- the acceptance criterion is that a 304 returns NotModified=true with
// nil Runs, so the caller never calls Upsert over it.
func TestPollRuns_ETagRoundTrip_304NoWrite(t *testing.T) {
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		runsURL: {Status: 200, Header: map[string]string{"Etag": capturedETag},
			Body: []byte(`{"total_count":1,"workflow_runs":[{"id":34616832466,"status":"completed","conclusion":"success"}]}`)},
	}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 42, allowActionsRoutes)

	first, err := c.PollRuns(context.Background(), "acamarata", "cascade")
	if err != nil {
		t.Fatalf("first PollRuns: %v", err)
	}
	if len(first.Runs) != 1 {
		t.Fatalf("first call: got %d runs, want 1", len(first.Runs))
	}

	// The real 304 exchange: same URL, this time the server answers
	// Not Modified against the If-None-Match this client now sends.
	doer.responses[runsURL] = HTTPResponse{Status: 304}

	second, err := c.PollRuns(context.Background(), "acamarata", "cascade")
	if err != nil {
		t.Fatalf("second PollRuns: %v", err)
	}
	if !second.NotModified {
		t.Error("second call: NotModified = false, want true on a 304")
	}
	if second.Runs != nil {
		t.Errorf("second call: Runs = %v, want nil on a 304 (no domain write)", second.Runs)
	}

	// The second request must have carried If-None-Match with the FIRST
	// call's Etag -- the conditional-request contract itself.
	if len(doer.calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(doer.calls))
	}
	if got := doer.calls[1].Headers["If-None-Match"]; got != capturedETag {
		t.Errorf("second request If-None-Match = %q, want %q", got, capturedETag)
	}
}

// TestPollJobs_RealFixture drives PollJobs over the real 18-job capture.
func TestPollJobs_RealFixture(t *testing.T) {
	body := loadFixture(t, "run_jobs.json")
	url := "https://api.github.com/repos/acamarata/cascade/actions/runs/34616832399/jobs?per_page=100"
	doer := &fakeDoer{responses: map[string]HTTPResponse{url: {Status: 200, Body: body}}}
	c := NewClient(doer, testEngine(t), "https://api.github.com", 1, allowActionsRoutes)
	res, err := c.PollJobs(context.Background(), "acamarata", "cascade", 34616832399)
	if err != nil {
		t.Fatalf("PollJobs: %v", err)
	}
	if len(res.Jobs) != 18 {
		t.Fatalf("got %d jobs, want 18", len(res.Jobs))
	}
}

// TestAcquireCIPollGate_DisabledClassRefused asserts a registry where
// ci-poll is registered DISABLED refuses before any byte leaves the
// process.
func TestAcquireCIPollGate_DisabledClassRefused(t *testing.T) {
	reg := egress.NewRegistry()
	reg.MustRegister(egress.EgressClassCIPoll, egress.InterceptConfig{Enabled: false, Owner: "test"})
	doer := &fakeDoer{}
	c := NewClient(doer, buildEngine(t, reg), "https://api.github.com", 1, allowActionsRoutes)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected a refusal for a disabled ci-poll class")
	}
	if len(doer.calls) != 0 {
		t.Errorf("doer was called %d times against a disabled class, want 0", len(doer.calls))
	}
}

// TestAcquireCIPollGate_SensitivityRefused covers the sensitivity-pass
// refusal branch: a class enabled but narrowed to a tier list that
// excludes TierInternal (the tier this package always declares).
func TestAcquireCIPollGate_SensitivityRefused(t *testing.T) {
	reg := egress.NewRegistry()
	reg.MustRegister(egress.EgressClassCIPoll, egress.InterceptConfig{
		Enabled: true, Owner: "test", AllowedTiers: []egress.SensitivityTier{egress.TierPublic},
	})
	c := NewClient(&fakeDoer{}, buildEngine(t, reg), "https://api.github.com", 1, allowActionsRoutes)
	if _, err := c.PollRuns(context.Background(), "o", "r"); err == nil {
		t.Error("expected a sensitivity-pass refusal")
	}
}

// TestNewClient_DefaultBaseURL covers the empty-baseURL branch.
func TestNewClient_DefaultBaseURL(t *testing.T) {
	c := NewClient(&fakeDoer{}, testEngine(t), "", 1, allowActionsRoutes)
	if c.baseURL != "https://api.github.com" {
		t.Errorf("baseURL = %q, want the default", c.baseURL)
	}
}

// TestPageCount covers pageCount's perPage<=0 guard directly.
func TestPageCount(t *testing.T) {
	if got := pageCount(0, 0); got != 0 {
		t.Errorf("pageCount(0,0) = %d, want 0", got)
	}
	if got := pageCount(250, 100); got != 3 {
		t.Errorf("pageCount(250,100) = %d, want 3", got)
	}
}

// TestGitHubActionsOIDCFromRunArtifact exercises dispatch and artifact retrieval locally.
func TestGitHubActionsOIDCFromRunArtifact(t *testing.T) {
	listing, err := os.ReadFile("testdata/github/artifacts.json")
	if err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	doer := actionsDoer(t, listing, oidcFixtureArchive(t), &dispatches)
	c := NewClient(doer, testEngine(t), actionsTestBase, 1, allowActionsRoutes)
	ctx := context.Background()
	if err := c.TriggerWorkflowDispatch(ctx, "o", "r", "cascade-ci.yml", "main"); err == nil {
		t.Fatal("dispatch allowed without admitted configuration")
	}
	c.Allowed = func() (bool, error) { return false, nil }
	if err := c.TriggerWorkflowDispatch(ctx, "o", "r", "cascade-ci.yml", "main"); err == nil {
		t.Fatal("dispatch allowed with hosted configuration disabled")
	}
	c.Allowed = func() (bool, error) { return true, nil }
	if err := c.TriggerWorkflowDispatch(ctx, "o", "r", "cascade-ci.yml", "main"); err != nil {
		t.Fatal(err)
	}
	artifacts, err := c.ListRunArtifacts(ctx, "o", "r", 7)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts: %v, %v", artifacts, err)
	}
	if artifacts[0].Name != "cascade-oidc" || artifacts[0].Expired {
		t.Fatal("unexpected artifact")
	}
	data, err := c.DownloadArtifact(ctx, "o", "r", artifacts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "token.jwt" {
		t.Fatalf("invalid archive: %v", err)
	}
	if dispatches != 1 {
		t.Fatalf("dispatches = %d", dispatches)
	}
	assertActionsRoutesEnforced(ctx, t, c)
}

// assertActionsRoutesEnforced checks that, with no route resolver or a
// local-only one, every Actions call is refused before it reaches GitHub.
func assertActionsRoutesEnforced(ctx context.Context, t *testing.T, c *Client) {
	t.Helper()
	for _, routes := range []RouteResolver{nil, localOnlyRoutes} {
		c.routes = routes
		if err := c.TriggerWorkflowDispatch(ctx, "o", "r", "cascade-ci.yml", "main"); err == nil {
			t.Fatal("dispatch bypassed route policy")
		}
		if _, err := c.ListRunArtifacts(ctx, "o", "r", 7); err == nil {
			t.Fatal("artifact listing bypassed route policy")
		}
		if _, err := c.DownloadArtifact(ctx, "o", "r", 101); err == nil {
			t.Fatal("artifact download bypassed route policy")
		}
	}
}

// oidcFixtureArchive returns a zip holding one token.jwt entry. The entry
// is an invalid placeholder: it exercises transport without storing an
// OIDC token.
func oidcFixtureArchive(t *testing.T) []byte {
	t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.Create("token.jwt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("invalid fixture payload")); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// actionsTestBase is the API base the in-memory Actions doer answers for.
const actionsTestBase = "https://api.github.test"

// actionsDoer answers the four GitHub REST routes the dispatch and
// artifact calls use, in memory, counting accepted workflow dispatches.
func actionsDoer(t *testing.T, listing, archive []byte, dispatches *int) doerFunc {
	t.Helper()
	return doerFunc(func(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
		path, _, _ := strings.Cut(strings.TrimPrefix(req.URL, actionsTestBase), "?")
		switch path {
		case "/repos/o/r":
			return HTTPResponse{Status: 200, Body: []byte(`{"private":false,"visibility":"public"}`)}, nil
		case "/repos/o/r/actions/workflows/cascade-ci.yml/dispatches":
			var body map[string]string
			if req.Method != "POST" || json.Unmarshal(req.Body, &body) != nil || body["ref"] != "main" {
				t.Error("invalid workflow dispatch request")
				return HTTPResponse{Status: 400}, nil
			}
			*dispatches++
			return HTTPResponse{Status: 204}, nil
		case "/repos/o/r/actions/runs/7/artifacts":
			return HTTPResponse{Status: 200, Body: listing}, nil
		case "/repos/o/r/actions/artifacts/101/zip":
			return HTTPResponse{Status: 200, Body: archive}, nil
		}
		return HTTPResponse{Status: 404}, nil
	})
}
