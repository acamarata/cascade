package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecodeGitHubResponse drives every GitHub response decoder with
// arbitrary bytes (06 §5.7: this ticket adds a decoder, so it carries a
// fuzz target).
//
// The contract under fuzzing is TOTALITY: for any input each decoder either
// returns a value or a typed error, and none panics. That matters because
// these bytes come off the network from a service this plugin does not
// control — a truncated response, an HTML error page from a proxy, or a
// shape GitHub changes without warning must all fail as errors rather than
// take the plugin down.
//
// Classify is included deliberately: it reads the same untrusted body to
// build its message, so a decoder that is total and a classifier that is
// not would still crash the plugin on the error path, which is exactly when
// it is least welcome.
func FuzzDecodeGitHubResponse(f *testing.F) {
	for _, name := range []string{
		"repos.get.json", "repos.list.json",
		"issues.list.json", "prs.list.json", "error.404.json",
	} {
		if raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixtures", name)); err == nil {
			f.Add(raw)
		}
	}
	f.Add([]byte(``))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"message":"Not Found"}`))
	f.Add([]byte(`<html>502</html>`))

	f.Fuzz(func(_ *testing.T, body []byte) {
		// Single-object decoders.
		if repo, err := DecodeRepo(body); err == nil {
			// A decode that succeeded must produce a usable value: calling
			// through it must not panic either.
			_, _ = CloneURL(repo)
		}
		_, _ = DecodeIssue(body)
		_, _ = DecodePullRequest(body)

		// List decoders, whose element loops are the part most likely to
		// fault on a partially-typed array.
		if issues, err := DecodeIssues(body); err == nil {
			for _, issue := range issues {
				_ = issue.IsPullRequest()
			}
		}
		if prs, err := DecodePullRequests(body); err == nil {
			for _, pr := range prs {
				_ = pr.IsMerged()
			}
		}
		_, _ = DecodeRepos(body)

		// The error path reads the same untrusted bytes.
		_ = DecodeAPIError(body)
		for _, status := range []int{200, 401, 403, 404, 422, 500} {
			_ = Classify(status, body)
		}
	})
}
