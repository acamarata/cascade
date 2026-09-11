package conductor

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// --- shared test doubles, used by every _test.go file in this package ---

type fakeRouter struct {
	selectFn func(ctx context.Context, req provider.ModelRequest, exclude ...string) (provider.Selection, error)
	calls    int
}

func (r *fakeRouter) Select(ctx context.Context, req provider.ModelRequest, exclude ...string) (provider.Selection, error) {
	r.calls++
	if r.selectFn != nil {
		return r.selectFn(ctx, req, exclude...)
	}
	return provider.Selection{LaneID: "lane-1", Provider: "test", Model: "test-model"}, nil
}

type fakeProvider struct {
	chatFn   func(context.Context, provider.ChatRequest) (provider.ChatResponse, error)
	streamFn func(context.Context, provider.ChatRequest, provider.StreamSink) error
	embedFn  func(context.Context, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error)
}

func (f *fakeProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if f.chatFn != nil {
		return f.chatFn(ctx, req)
	}
	return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}}, nil
}
func (f *fakeProvider) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	if f.embedFn != nil {
		return f.embedFn(ctx, req)
	}
	return provider.ModelEmbedResponse{}, nil
}
func (f *fakeProvider) Count(context.Context, provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, nil
}
func (f *fakeProvider) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) error {
	if f.streamFn != nil {
		return f.streamFn(ctx, req, sink)
	}
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}
func (f *fakeProvider) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, nil
}

type fakeResolver struct {
	provider provider.ModelProvider
	err      error
}

func (r *fakeResolver) Resolve(context.Context, provider.Selection) (provider.ModelProvider, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.provider, nil
}

type fakeClassifier struct{ err error }

func (c *fakeClassifier) Classify(context.Context, provider.ModelRequest) error { return c.err }

type fakeTaxonomy struct{}

func (fakeTaxonomy) Classes() []string { return []string{"chat"} }

type fakePolicy struct{ err error }

func (p *fakePolicy) Authorize(context.Context, provider.ModelRequest) error { return p.err }

type fakeSensitivityGate struct{}

func (fakeSensitivityGate) Resolve(tier provider.SensitivityTier) provider.SensitivityTier {
	return tier
}

type spyAudit struct {
	mu     sync.Mutex
	events []audit.Event
	err    error
}

func (s *spyAudit) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	if s.err != nil {
		return audit.Record{}, s.err
	}
	return audit.Record{Seq: uint64(len(s.events))}, nil
}

func (s *spyAudit) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

type testVault struct{}

func (testVault) List(context.Context) ([]string, error) { return nil, nil }
func (testVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "test vault: empty")
}

func validReq() provider.ModelRequest {
	return provider.ModelRequest{
		TaskID:    "task-1",
		TaskClass: "chat",
		Inputs:    []provider.ChatMessage{{Role: "user", Content: "hi"}},
	}
}

// readyDeps bundles every collaborator newReadyExecutor wires, so a test
// can mutate one before construction or reach into a fake after dispatch.
type readyDeps struct {
	router   *fakeRouter
	resolver *fakeResolver
	prov     *fakeProvider
	audit    *spyAudit
}

func newReadyConfig(t *testing.T) (ExecutorConfig, *readyDeps) {
	t.Helper()
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	firewall, err := egress.NewEngine(egress.DefaultRegistry(), testVault{}, detector)
	if err != nil {
		t.Fatalf("egress.NewEngine: %v", err)
	}
	fp := &fakeProvider{}
	deps := &readyDeps{
		router:   &fakeRouter{},
		resolver: &fakeResolver{provider: fp},
		prov:     fp,
		audit:    &spyAudit{},
	}
	cfg := ExecutorConfig{
		Router: deps.router, Resolver: deps.resolver,
		Classifier: &fakeClassifier{}, Taxonomy: fakeTaxonomy{},
		Policy: &fakePolicy{}, Audit: deps.audit,
		Sensitivity: fakeSensitivityGate{}, Firewall: firewall,
		Clock: runtime.NewFixedClock(time.Unix(0, 0)),
	}
	return cfg, deps
}

func newReadyExecutor(t *testing.T) (*Executor, *readyDeps) {
	t.Helper()
	cfg, deps := newReadyConfig(t)
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return exec, deps
}

// TestExecute_ModelTypesLiveInPkgProvider asserts the conductor Executor
// satisfies provider.ModelExecutor and that no conductor-local copy of
// ModelRequest, ModelResponse, Selection or SensitivityTier exists
// (R-21.264, R-40.X8): those four SDK types are declared exactly once, in
// pkg/provider/model.go.
func TestExecute_ModelTypesLiveInPkgProvider(t *testing.T) {
	var _ provider.ModelExecutor = (*Executor)(nil)

	banned := map[string]bool{
		"ModelRequest": true, "ModelResponse": true,
		"Selection": true, "SensitivityTier": true,
	}
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing internal/conductor: %v", err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if ok && banned[ts.Name.Name] {
					t.Errorf("internal/conductor declares a local copy of SDK type %s; it must live only in pkg/provider/model.go", ts.Name.Name)
				}
			}
		}
	}
}

// TestExecute_JobIDSingleDefinition asserts conductor.JobID is a true type
// alias of provider.JobID (R-21.281), not a second definition.
func TestExecute_JobIDSingleDefinition(t *testing.T) {
	var j JobID = "example"
	if reflect.TypeOf(j) != reflect.TypeOf(provider.JobID("example")) {
		t.Fatalf("conductor.JobID is not an alias of provider.JobID: got %v", reflect.TypeOf(j))
	}
	if provider.JobID(j) != "example" {
		t.Fatalf("alias round-trip failed: got %q", provider.JobID(j))
	}
}
