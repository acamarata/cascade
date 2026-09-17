package coretools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// fixedClock is the memory store's clock. It is frozen so a record's
// address never depends on when the suite ran.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC) }

// liveRegistry builds a real RPC registry serving the memory.* namespace
// over a temp-dir store — the SAME memory.NewHandler(...).Register the
// daemon composition root calls, not a double of it.
func liveRegistry(t *testing.T) *rpc.Registry {
	t.Helper()
	registry := rpc.NewRegistry()
	memory.NewHandler(memory.NewFileStore(t.TempDir(), fixedClock{}), fixedClock{}).Register(registry)
	return registry
}

// noPlugins is an empty manifest source: these tests exercise the
// first-party registrations, not whatever plugins happen to be linked in.
func noPlugins() []plugin.BuiltinRegistration { return nil }

// grantingFilter allows exactly the capabilities it was given.
type grantingFilter map[string]bool

func (g grantingFilter) Allow(_ context.Context, capability string) bool { return g[capability] }

// TestMCPToolPolicyFilter is the exposure gate: a granted capability puts
// its tools in the manifest, an ungranted one leaves them out, and an
// unknown capability is denied rather than defaulted.
//
// Three registries over ONE registration set, so a filter that was held
// but never consulted produces the same manifest three times and fails.
func TestMCPToolPolicyFilter(t *testing.T) {
	regs := coretools.Registrations(liveRegistry(t))
	if len(regs) == 0 {
		t.Fatal("no memory tool registered against a registry that serves memory.*")
	}

	granted := mcp.NewToolRegistry(noPlugins, grantingFilter{
		coretools.CapabilityMemoryRead: true, coretools.CapabilityMemoryWrite: true,
	}, regs...)
	if len(granted.List()) != len(regs) {
		t.Fatalf("granted registry lists %d of %d tools", len(granted.List()), len(regs))
	}
	if filtered := granted.FilteredOut(); len(filtered) != 0 {
		t.Fatalf("a fully granted registry filtered %v", filtered)
	}

	readOnly := mcp.NewToolRegistry(noPlugins, grantingFilter{
		coretools.CapabilityMemoryRead: true,
	}, regs...)
	assertAbsent(t, readOnly, "cascade_memory_remember", "cascade_memory_forget")
	assertPresent(t, readOnly, "cascade_memory_recall")
	if len(readOnly.FilteredOut()) == 0 {
		t.Fatal("the write tools were withheld but FilteredOut names none of them")
	}

	// An empty grant map is the "unknown capability" case: nothing this
	// filter was told about, so nothing it can positively allow.
	unknown := mcp.NewToolRegistry(noPlugins, grantingFilter{}, regs...)
	if listed := unknown.List(); len(listed) != 0 {
		t.Fatalf("an ungranted registry still listed %d tools", len(listed))
	}
}

// TestAnUnfilteredRegistryExposesNoCapabilityTool pins the default. A
// composition root that forgets to wire a filter must expose nothing that
// needs a capability, never everything.
func TestAnUnfilteredRegistryExposesNoCapabilityTool(t *testing.T) {
	regs := coretools.Registrations(liveRegistry(t))
	if listed := mcp.NewToolRegistry(noPlugins, mcp.DenyAllFilter{}, regs...).List(); len(listed) != 0 {
		t.Fatalf("a registry with no capability filter listed %d capability-gated tools", len(listed))
	}
}

// TestADeniedToolIsIndistinguishableFromAnAbsentOne asserts the leak this
// filter must not have: a client that could tell "denied" from "does not
// exist" would learn that a privileged tool exists on this machine.
func TestADeniedToolIsIndistinguishableFromAnAbsentOne(t *testing.T) {
	regs := coretools.Registrations(liveRegistry(t))
	denied := mcp.NewToolRegistry(noPlugins, grantingFilter{}, regs...)

	_, gatedErr := denied.Call(context.Background(), "cascade_memory_recall", []byte(`{"query":"x","k":1}`))
	_, inventedErr := denied.Call(context.Background(), "cascade_tool_that_never_existed", []byte(`{}`))
	if gatedErr == nil || inventedErr == nil {
		t.Fatal("a denied or unknown tool answered instead of refusing")
	}
	if strings.Replace(gatedErr.Error(), "cascade_memory_recall", "X", 1) !=
		strings.Replace(inventedErr.Error(), "cascade_tool_that_never_existed", "X", 1) {
		t.Fatalf("a denied tool refuses differently from an absent one:\n denied: %v\n absent: %v", gatedErr, inventedErr)
	}
}

// TestMCPInstructionContractTools is the live round-trip: a tool call
// reaching the real RPC handler and coming back with the real answer.
//
// It writes through cascade_memory_remember and reads back through
// cascade_memory_recall, against one real file-backed store, so a broken
// adapter fails here rather than in production. The pair matters: a
// handler that returned a plausible empty result would pass a read-only
// assertion and fail this one.
func TestMCPInstructionContractTools(t *testing.T) {
	registry := liveRegistry(t)
	tools := mcp.NewToolRegistry(noPlugins, grantingFilter{
		coretools.CapabilityMemoryRead: true, coretools.CapabilityMemoryWrite: true,
	}, coretools.Registrations(registry)...)

	ctx := context.Background()
	body := "the placement engine reads DeviceRecord.LastReport"
	written, err := tools.Call(ctx, "cascade_memory_remember",
		[]byte(`{"content":`+quote(body)+`,"type":"project","name":"round-trip-probe"}`))
	if err != nil {
		t.Fatalf("cascade_memory_remember: %v", err)
	}
	var remembered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(written, &remembered); err != nil {
		t.Fatalf("decode remember result: %v", err)
	}
	if remembered.ID == "" {
		t.Fatalf("remember returned no address: %s", written)
	}

	read, err := tools.Call(ctx, "cascade_memory_recall", []byte(`{"query":"placement engine","k":5}`))
	if err != nil {
		t.Fatalf("cascade_memory_recall: %v", err)
	}
	// memory.recall answers with whole records rather than addresses, so
	// the assertion is on the record's own name and body, and on BOTH:
	// matching the body alone would pass for a handler that echoed the
	// query's neighbourhood, and matching the name alone would pass for
	// one that returned an empty shell under the right name.
	if !strings.Contains(string(read), `"Name":"round-trip-probe"`) || !strings.Contains(string(read), body) {
		t.Fatalf("recall did not return the record just written (%s):\n%s", remembered.ID, read)
	}
}

// quote renders s as a JSON string literal.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestARefusalKeepsItsKind proves the adapter carries the method's own
// taxonomy classification across the boundary rather than flattening
// every failure to "internal".
func TestARefusalKeepsItsKind(t *testing.T) {
	tools := mcp.NewToolRegistry(noPlugins, grantingFilter{
		coretools.CapabilityMemoryRead: true,
	}, coretools.Registrations(liveRegistry(t))...)

	// An unparseable params object is the cheapest request that reaches
	// the method and is refused BY it — a malformed k, which the method's
	// own decoder rejects rather than defaulting.
	_, err := tools.Call(context.Background(), "cascade_memory_recall", []byte(`{"query":"x","k":"not-a-number"}`))
	if err == nil {
		t.Fatal("memory.recall accepted a non-numeric k")
	}
	if !strings.Contains(err.Error(), "memory.recall") {
		t.Fatalf("the refusal does not name the method that made it: %v", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v (typed %t), want KindInvalidInput carried across the adapter", kind, ok)
	}
}

// TestAToolWhoseMethodIsUnservedIsNotRegistered is the Art.1 gate stated
// as a test: a registry serving nothing registers nothing, rather than
// registering tools that answer "method not found".
func TestAToolWhoseMethodIsUnservedIsNotRegistered(t *testing.T) {
	empty := rpc.NewRegistry()
	if regs := coretools.Registrations(empty); len(regs) != 0 {
		t.Fatalf("an empty method table still produced %d registrations", len(regs))
	}
	if unservable := coretools.Unservable(empty); len(unservable) != len(coretools.Specs()) {
		t.Fatalf("Unservable named %d specs, want all %d", len(unservable), len(coretools.Specs()))
	}
	if regs := coretools.Registrations(nil); regs != nil {
		t.Fatal("a nil dispatcher produced registrations")
	}
}

// TestThePolicyFilterFailsClosed walks the production filter's four
// refusal axes. A real engine is not needed for three of them, and the
// fourth (a verdict short of allow) is exercised through a stub Evaluator
// so the assertion does not depend on which grants a machine happens to
// hold.
func TestThePolicyFilterFailsClosed(t *testing.T) {
	subject := policy.Subject{Kind: policy.SubjectUser, ID: "tester"}
	ctx := context.Background()

	if coretools.NewPolicyFilter(nil, subject).Allow(ctx, coretools.CapabilityMemoryRead) {
		t.Error("a filter with no engine allowed a capability")
	}
	if coretools.NewPolicyFilter(stubEvaluator{verdict: policy.VerdictAllow}, subject).Allow(ctx, "") {
		t.Error("a filter allowed the empty capability")
	}
	for _, verdict := range []policy.Verdict{policy.VerdictAsk, policy.VerdictDeny} {
		if coretools.NewPolicyFilter(stubEvaluator{verdict: verdict}, subject).Allow(ctx, coretools.CapabilityMemoryRead) {
			t.Errorf("a %v verdict exposed the tool", verdict)
		}
	}
	if coretools.NewPolicyFilter(stubEvaluator{err: errStub}, subject).Allow(ctx, coretools.CapabilityMemoryRead) {
		t.Error("an evaluation error exposed the tool")
	}
	if !coretools.NewPolicyFilter(stubEvaluator{verdict: policy.VerdictAllow}, subject).Allow(ctx, coretools.CapabilityMemoryRead) {
		t.Error("an allow verdict did not expose the tool; the filter refuses everything")
	}
}

// stubEvaluator answers with a fixed verdict or error.
type stubEvaluator struct {
	verdict policy.Verdict
	err     error
}

func (s stubEvaluator) Evaluate(context.Context, policy.EvalRequest) (policy.EvalOutcome, error) {
	if s.err != nil {
		return policy.EvalOutcome{}, s.err
	}
	return policy.EvalOutcome{Verdict: s.verdict}, nil
}

// errStub is the evaluation failure the fail-closed walk injects.
var errStub = &stubError{}

type stubError struct{}

func (*stubError) Error() string { return "policy: evaluation failed" }

// assertPresent fails unless every name is in the registry's list.
func assertPresent(t *testing.T, r *mcp.ToolRegistry, names ...string) {
	t.Helper()
	listed := map[string]bool{}
	for _, tool := range r.List() {
		listed[tool.Name] = true
	}
	for _, name := range names {
		if !listed[name] {
			t.Errorf("%q is absent from the manifest", name)
		}
	}
}

// assertAbsent fails if any name is in the registry's list.
func assertAbsent(t *testing.T, r *mcp.ToolRegistry, names ...string) {
	t.Helper()
	for _, tool := range r.List() {
		for _, name := range names {
			if tool.Name == name {
				t.Errorf("%q is in the manifest but its capability was not granted", name)
			}
		}
	}
}
