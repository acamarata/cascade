package review

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the CR fix D8 proof. The draft's Art.2 fixture was hand-authored
//   (disclosed) AND decoded through the TEST's own struct, so a
//   wrong-protocol fixture would still have passed: its task_class,
//   requirements and sensitivity were never checked against anything real.
//   Here the reviewer's own request is encoded by the REAL wire codec
//   (pkg/provider.Client.ModelExecute's toWireParams, reached by dialing a
//   Client rather than by re-typing its shape), the recorded response is
//   decoded by the REAL response path (the same json decode internal/client's
//   Do performs into a *ModelResponse), and every wire field is asserted
//   against the REAL §5.16 row.
// Constraints: Art.2 honest floor -- no live daemon and no credentials are
//   available to a unit lane (testdata/README.md records this), so what is
//   real here is the CODEC and the taxonomy, not the network.
// SPORT: internal/review.codec-proof (ADD, P1-E25-W5-S52-T4).

// fixtureExchange mirrors testdata/fixtures/cr-review-session.json. The
// conductor_exchange half is deliberately captured as RAW JSON for the
// response, so the assertion below decodes it through provider.ModelResponse
// itself rather than through a hand-written mirror of it.
type fixtureExchange struct {
	Request struct {
		Level   string `json:"level"`
		Diff    string `json:"diff"`
		Context string `json:"context"`
	} `json:"request"`
	ConductorExchange struct {
		TaskClass    string                `json:"task_class"`
		Requirements provider.Requirements `json:"requirements"`
		Sensitivity  string                `json:"sensitivity"`
		Response     json.RawMessage       `json:"response"`
	} `json:"conductor_exchange"`
}

func loadFixture(t *testing.T) fixtureExchange {
	t.Helper()
	raw, err := os.ReadFile("testdata/fixtures/cr-review-session.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx fixtureExchange
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return fx
}

// codecRPC is a pkg/provider.RPCCaller double: it captures the params the
// REAL codec produced and decodes the fixture's recorded response into out
// exactly as internal/client.Client.Do does.
type codecRPC struct {
	method   string
	params   []json.RawMessage
	response json.RawMessage
}

func (c *codecRPC) Do(_ context.Context, method string, params, out any) error {
	c.method = method
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.params = append(c.params, encoded)
	if out == nil || len(c.response) == 0 {
		return nil
	}
	return json.Unmarshal(c.response, out)
}

// codecExecutor routes every Execute through the REAL
// pkg/provider.Client.ModelExecute -- the same door
// internal/plugins/review_wiring.go's production executor dials.
type codecExecutor struct{ rpc *codecRPC }

func (e codecExecutor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	return provider.NewClient(e.rpc).ModelExecute(ctx, req)
}

// TestReviewProviderRealCounterpart_WireCodec is the Art.2 real-counterpart
// test, now with a real codec under it: Provider.Review is invoked through the
// pkg/provider.ReviewProvider interface (the exact path an external caller
// such as cascade-pbd uses), every dispatch is encoded by
// Client.ModelExecute's own wire translation, and the resulting wire params
// are asserted against the real §5.16 review row and the fixture's recorded
// exchange.
func TestReviewProviderRealCounterpart_WireCodec(t *testing.T) {
	fx := loadFixture(t)
	rpc := &codecRPC{response: fx.ConductorExchange.Response}
	concrete, err := NewProvider(codecExecutor{rpc: rpc}, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	var rp provider.ReviewProvider = concrete

	resp, err := rp.Review(context.Background(), provider.ReviewRequest{
		Level:   provider.ReviewCRLevel(fx.Request.Level),
		Diff:    fx.Request.Diff,
		Context: fx.Request.Context,
	})
	if err != nil {
		t.Fatalf("ReviewProvider.Review (real-counterpart fixture): %v", err)
	}
	if len(resp.Findings) == 0 {
		t.Fatal("real-counterpart fixture produced zero findings")
	}
	if resp.Findings[0].Severity != provider.ReviewSeverityMajor {
		t.Errorf("finding severity = %q, want %q (per the fixture)", resp.Findings[0].Severity, provider.ReviewSeverityMajor)
	}
	if rpc.method != "conductor.execute" {
		t.Errorf("the dispatch dialled %q, want the daemon's real conductor.execute door", rpc.method)
	}
	if len(rpc.params) != 1 {
		t.Fatalf("CR-B made %d dispatches, want 1", len(rpc.params))
	}
	requireWireMatchesTaxonomy(t, rpc.params[0], conductor.TaskClassReview, fx)
}

// requireWireMatchesTaxonomy decodes the bytes the REAL codec produced and
// asserts task_class, requirements and sensitivity against the real §5.16 row
// -- and against the fixture's own recorded values, so a fixture that drifts
// from the protocol fails instead of being believed.
func requireWireMatchesTaxonomy(t *testing.T, params json.RawMessage, class conductor.TaskClass, fx fixtureExchange) {
	t.Helper()
	var wire struct {
		TaskID       string                `json:"task_id"`
		TaskClass    string                `json:"task_class"`
		Requirements provider.Requirements `json:"requirements"`
		Sensitivity  string                `json:"sensitivity"`
		Inputs       []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(params, &wire); err != nil {
		t.Fatalf("decode the real codec's wire params: %v", err)
	}
	row, ok := rowFor(conductor.TaskClasses(), class)
	if !ok {
		t.Fatalf("the real §5.16 table has no %q row", class)
	}
	if wire.TaskClass != row.Class {
		t.Errorf("wire task_class = %q, want the row's %q", wire.TaskClass, row.Class)
	}
	if wire.Sensitivity != row.SensitivityDefault.String() {
		t.Errorf("wire sensitivity = %q, want the %q row's own default %q",
			wire.Sensitivity, class, row.SensitivityDefault.String())
	}
	if !reflect.DeepEqual(wire.Requirements, provider.Requirements{
		Reasoning: row.Reasoning, Context: row.CtxK * 1000, Structured: row.Structured,
	}) {
		t.Errorf("wire requirements = %+v, want the row's own {%s, %d, %v}",
			wire.Requirements, row.Reasoning, row.CtxK*1000, row.Structured)
	}
	if wire.TaskClass != fx.ConductorExchange.TaskClass {
		t.Errorf("wire task_class = %q but the fixture records %q: the fixture is off-protocol",
			wire.TaskClass, fx.ConductorExchange.TaskClass)
	}
	if wire.Sensitivity != fx.ConductorExchange.Sensitivity {
		t.Errorf("wire sensitivity = %q but the fixture records %q: the fixture is off-protocol",
			wire.Sensitivity, fx.ConductorExchange.Sensitivity)
	}
	if !reflect.DeepEqual(wire.Requirements, fx.ConductorExchange.Requirements) {
		t.Errorf("wire requirements = %+v but the fixture records %+v: the fixture is off-protocol",
			wire.Requirements, fx.ConductorExchange.Requirements)
	}
	if len(wire.Inputs) != 1 || wire.Inputs[0].Role != "user" || wire.Inputs[0].Content == "" {
		t.Errorf("wire inputs = %+v, want exactly one user turn with content", wire.Inputs)
	}
}

// TestReviewerNeverMutatesTheAuthorsScope is the CR #13 assertion at the
// package level: the reviewer has no write path at all. It holds no worktree
// handle, opens no file, and the only thing it emits is a ModelRequest whose
// Inputs carry the author's own blocks verbatim. The structural half (no file
// or exec call anywhere in the package's non-test source) is checked by
// scanning this package's own source for the write primitives, so a future
// edit that adds one turns this red.
func TestReviewerNeverMutatesTheAuthorsScope(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	forbidden := []string{"os.WriteFile", "os.Create", "os.Remove", "os.Rename", "os.OpenFile",
		"exec.Command", "ioutil.WriteFile"}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, f := range forbidden {
			if strings.Contains(string(src), f) {
				t.Errorf("%s calls %s: the reviewer reports findings and never applies them, so it has no "+
					"write path to the author's worktree (R-16.32 read-only lease)", name, f)
			}
		}
	}

	// And the author's own blocks reach the model unrewritten: same
	// assertion as artifact_test.go's byte-identical check, made here
	// against the request the ABI path actually dispatches.
	reqContext := "files_scope:\n  add:\n  - a.go\ntasks:\n- t1\nacceptance_criteria:\n- ac1"
	rpc := &codecRPC{response: json.RawMessage(`{"output":"{\"approved\":true,\"findings\":[]}","selection":{"provider":"anthropic"}}`)}
	p, err := NewProvider(codecExecutor{rpc: rpc}, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, err := p.Review(context.Background(), provider.ReviewRequest{
		Level: provider.ReviewCRLevelB, Diff: "diff --git a/a.go b/a.go\n+x", Context: reqContext,
	}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(rpc.params) != 1 || !strings.Contains(string(rpc.params[0]), strings.ReplaceAll(strings.ReplaceAll(reqContext, "\n", `\n`), `"`, `\"`)) {
		t.Errorf("the author's files_scope/tasks/acceptance_criteria block did not reach the wire verbatim:\n%s", rpc.params[0])
	}
}
