//go:build !windows && integration

package conversation

// Purpose: writeResumeFixture, the `-update` fixture-regeneration helper
//   for TestResume_RealSocket_FullFlow
//   (resume_integration_test.go) -- split out under Art.10.3's 300-line
//   cap. Same build tag as its caller: this file compiles only alongside
//   the integration-tagged test that uses it.
// SPORT: internal.conversation.resume/ADDED (integration test)
//   (P1-E22-W5-S47-T2).

import (
	"encoding/json"
	"flag"
	"os"
	"testing"
)

// updateResumeFixture is the -update flag: `go test -tags integration -run TestResume_RealSocket_FullFlow
// -update` regenerates testdata/resume-fixtures/example-chat-resume-response.json
// from TestResume_RealSocket_FullFlow's own live daemon response. The
// DEFAULT run (this flag unset) never writes -- it only compares, both
// ways, against whatever the fixture already carries.
var updateResumeFixture = flag.Bool("update", false, "regenerate the resume fixture from this test's live response")

// writeResumeFixture writes fields -- the live resumeResult's own
// JSON, unmodified -- back to path as the new fixture, with a
// "_provenance" field naming this test by its exact function name (the
// contract's own required wording).
func writeResumeFixture(t *testing.T, path string, fields map[string]json.RawMessage) {
	t.Helper()
	out := make(map[string]json.RawMessage, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	provenance, err := json.Marshal("Representative example of chat.resume's wire response shape, recorded from " +
		"TestResume_RealSocket_FullFlow (resume_integration_test.go) via `-update`. Not used for golden-diffing: " +
		"tokens/trimmed vary with real thread content and the injected TokenCounter, so a " +
		"byte-frozen comparison would go stale by construction. The default test run compares this file's field " +
		"set against a fresh live response, both ways.")
	if err != nil {
		t.Fatalf("marshal provenance: %v", err)
	}
	out["_provenance"] = provenance
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal updated fixture: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write updated fixture: %v", err)
	}
}
