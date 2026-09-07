package intake

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
)

// fakeDoer is a recording, in-memory Doer (no net/http import; Art.7.2).
type fakeDoer struct {
	responses map[string]HTTPResponse
	calls     []HTTPRequest
	err       map[string]error
}

func (f *fakeDoer) Do(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
	f.calls = append(f.calls, req)
	if err, ok := f.err[req.URL]; ok {
		return HTTPResponse{}, err
	}
	if resp, ok := f.responses[req.URL]; ok {
		return resp, nil
	}
	return HTTPResponse{Status: 404, Body: []byte("not found")}, nil
}

// testEngine builds a real *egress.Engine over the default registry.
func testEngine(t *testing.T) *egress.Engine {
	t.Helper()
	return testEngineWithRegistry(t, egress.DefaultRegistry())
}

// testEngineWithRegistry builds an engine over an arbitrary registry (used
// for the unregistered/disabled provider-intake class tests).
func testEngineWithRegistry(t *testing.T, reg *egress.Registry) *egress.Engine {
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

// nopVault satisfies egress.Vault with no stored values.
type nopVault struct{}

func (nopVault) List(_ context.Context) ([]string, error)        { return nil, nil }
func (nopVault) Get(_ context.Context, _ string) ([]byte, error) { return nil, nil }

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// TestShapeProbeOrder* covers the normative probe order, split into several
// funcs sharing the "TestShapeProbeOrder" prefix (`-run TestShapeProbeOrder`
// selects all of them) to stay under funlen's 50-line cap.
func TestShapeProbeOrderAnthropicFirst(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 200, Body: loadFixture(t, "probe_anthropic.golden.json")},
	}}
	kind, base, _, err := shapeProbe(context.Background(), doer, engine, "sk-test", "")
	if err != nil {
		t.Fatalf("shapeProbe: %v", err)
	}
	if kind != DriverAnthropic || base != "https://api.anthropic.com" {
		t.Fatalf("got (%s, %s)", kind, base)
	}
	if len(doer.calls) != 1 {
		t.Fatalf("expected exactly 1 call when anthropic matches first, got %d", len(doer.calls))
	}
}

func TestShapeProbeOrderOpenAISecond(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 401},
		"https://api.openai.com/v1/models":    {Status: 200, Body: loadFixture(t, "probe_openai_compat.golden.json")},
	}}
	kind, _, _, err := shapeProbe(context.Background(), doer, engine, "sk-test", "")
	if err != nil {
		t.Fatalf("shapeProbe: %v", err)
	}
	if kind != DriverOpenAICompat {
		t.Fatalf("got %s", kind)
	}
	if len(doer.calls) != 2 {
		t.Fatalf("expected exactly 2 calls (anthropic then openai), got %d", len(doer.calls))
	}
	if doer.calls[0].URL != "https://api.anthropic.com/v1/models" {
		t.Fatalf("expected anthropic tried first, got %s", doer.calls[0].URL)
	}
}

func TestShapeProbeOrderGeminiThird(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models":                                 {Status: 401},
		"https://api.openai.com/v1/models":                                    {Status: 401},
		"https://generativelanguage.googleapis.com/v1beta/models?key=sk-test": {Status: 200, Body: loadFixture(t, "probe_gemini.golden.json")},
	}}
	kind, _, _, err := shapeProbe(context.Background(), doer, engine, "sk-test", "")
	if err != nil {
		t.Fatalf("shapeProbe: %v", err)
	}
	if kind != DriverGemini {
		t.Fatalf("got %s", kind)
	}
	if len(doer.calls) != 3 {
		t.Fatalf("expected exactly 3 calls, got %d", len(doer.calls))
	}
}

func TestShapeProbeOrderAllFail(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://api.anthropic.com/v1/models": {Status: 401},
		"https://api.openai.com/v1/models":    {Status: 403},
	}}
	_, _, _, err := shapeProbe(context.Background(), doer, engine, "sk-test", "")
	if err == nil {
		t.Fatal("expected an error when every endpoint fails")
	}
	msg := err.Error()
	for _, want := range []string{"401", "403", "anthropic", "openai-compat", "gemini"} {
		if !strings.Contains(msg, want) {
			t.Errorf("all-fail error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "sk-test") {
		t.Fatalf("the supplied credential leaked into the all-fail error message: %q", msg)
	}
}

func TestShapeProbeOrderBaseURLOverride(t *testing.T) {
	engine := testEngine(t)
	doer := &fakeDoer{responses: map[string]HTTPResponse{
		"https://custom.example/v1/models": {Status: 200, Body: loadFixture(t, "probe_anthropic.golden.json")},
	}}
	kind, base, _, err := shapeProbe(context.Background(), doer, engine, "sk-test", "https://custom.example")
	if err != nil {
		t.Fatalf("shapeProbe: %v", err)
	}
	if kind != DriverAnthropic || base != "https://custom.example" {
		t.Fatalf("got (%s, %s)", kind, base)
	}
}

func TestShapeProbeRefusesUnregisteredClass(t *testing.T) {
	engine := testEngineWithRegistry(t, egress.NewRegistry()) // provider-intake never registered
	doer := &fakeDoer{}
	_, _, _, err := shapeProbe(context.Background(), doer, engine, "sk-test", "")
	if err == nil {
		t.Fatal("expected a refusal for an unregistered class")
	}
	if len(doer.calls) != 0 {
		t.Fatalf("expected zero outbound sockets for an unregistered class, got %d", len(doer.calls))
	}
}

func TestShapeProbeRefusesDisabledClass(t *testing.T) {
	reg := egress.NewRegistry()
	if err := reg.Register(egress.EgressClassProviderIntake, egress.InterceptConfig{Enabled: false, Owner: "test"}); err != nil {
		t.Fatalf("registering a disabled class: %v", err)
	}
	engine := testEngineWithRegistry(t, reg)
	doer := &fakeDoer{}
	_, _, _, err := shapeProbe(context.Background(), doer, engine, "sk-test", "")
	if err == nil {
		t.Fatal("expected a refusal for a disabled class")
	}
	if len(doer.calls) != 0 {
		t.Fatalf("expected zero outbound sockets for a disabled class, got %d", len(doer.calls))
	}
}

func TestModelsFromProbeGoldens(t *testing.T) {
	for _, c := range []struct {
		kind      DriverKind
		fixture   string
		firstWant string
	}{
		{DriverAnthropic, "probe_anthropic.golden.json", "claude-3-5-sonnet-20241022"},
		{DriverOpenAICompat, "probe_openai_compat.golden.json", "gpt-4o"},
		{DriverGemini, "probe_gemini.golden.json", "models/gemini-1.5-pro"},
	} {
		models, err := modelsFromProbe(c.kind, loadFixture(t, c.fixture))
		if err != nil {
			t.Fatalf("%s: modelsFromProbe: %v", c.kind, err)
		}
		if len(models) != 2 || models[0] != c.firstWant {
			t.Fatalf("%s: unexpected models: %v", c.kind, models)
		}
	}
}

func TestModelsFromProbeUnknownShapeFailsClosed(t *testing.T) {
	if _, err := modelsFromProbe(DriverAnthropic, []byte("not json")); err == nil {
		t.Fatal("expected a refusal for an undecodable body")
	}
}

func TestMicroVerifyRequestShapesPerDriver(t *testing.T) {
	cases := []struct {
		kind       DriverKind
		wantURL    string
		wantHeader string
	}{
		{DriverAnthropic, "https://api.anthropic.com/v1/messages", "x-api-key"},
		{DriverGemini, "https://api.example/v1beta/models/gemini-1.5-pro:generateContent?key=sk-test", "content-type"},
		{DriverOpenAICompat, "https://api.openai.com/v1/chat/completions", "Authorization"},
	}
	for _, c := range cases {
		base := "https://api.anthropic.com" // DriverAnthropic default
		switch c.kind {
		case DriverAnthropic:
		case DriverOpenAICompat, DriverOllama, DriverLocalLLM:
			base = "https://api.openai.com"
		case DriverGemini:
			base = "https://api.example"
		}
		model := "some-model"
		if c.kind == DriverGemini {
			model = "gemini-1.5-pro"
		}
		req := microVerifyRequest(c.kind, base, "sk-test", model)
		if req.URL != c.wantURL {
			t.Errorf("%s: expected URL %q, got %q", c.kind, c.wantURL, req.URL)
		}
		if _, ok := req.Headers[c.wantHeader]; !ok {
			t.Errorf("%s: expected header %q, got %v", c.kind, c.wantHeader, req.Headers)
		}
		if len(req.Body) == 0 {
			t.Errorf("%s: expected a non-empty request body", c.kind)
		}
	}
}

func TestHTTPDoerIsProductionOnly(t *testing.T) {
	// This test proves newHTTPDoer/NewHTTPDoer builds without a panic; the
	// no-network unit lane gate itself asserts no *_test.go file in this
	// package imports net/http (this file does not).
	d := NewHTTPDoer(0)
	if d == nil {
		t.Fatal("NewHTTPDoer must not return nil")
	}
}

func TestHTTPDoerRefusesAnUnbuildableRequest(t *testing.T) {
	// Validated locally before any dial: no socket opens (no-network lane).
	d := NewHTTPDoer(0)
	_, err := d.Do(context.Background(), HTTPRequest{Method: "BAD\nMETHOD", URL: "https://example.test"})
	if err == nil {
		t.Fatal("expected a refusal for an unbuildable request")
	}
}

// FuzzShapeProbeResponse fuzzes modelsFromProbe (R-21.266): a malformed
// body must return a typed error, never a panic.
func FuzzShapeProbeResponse(f *testing.F) {
	seed, err := os.ReadFile("testdata/probe_anthropic.golden.json")
	if err != nil {
		f.Fatalf("reading seed fixture: %v", err)
	}
	f.Add(seed)
	f.Add([]byte("{}"))
	f.Add([]byte("not json"))
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, kind := range []DriverKind{DriverAnthropic, DriverOpenAICompat, DriverGemini} {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("modelsFromProbe(%s, ...) panicked on %q: %v", kind, body, r)
				}
			}()
			_, _ = modelsFromProbe(kind, body)
		}
	})
}
