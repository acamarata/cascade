package conductor

// Purpose (this file): the second half of the door hardening regressions -
//   model names substituted, both failover attempts persisted in the real
//   usage store, a refusal audited for a cancelled caller, the Execute tier
//   joining the thread privacy mode, leaf cancellation carrying its cause, a
//   conversation whose messages each hold a secret, and the selection model.
//
// SPORT: conductor.door (TEST) - P1-SEC-01.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestDoorSubstitutesTheModelName(t *testing.T) {
	ctx := context.Background()
	sel := provider.Selection{LaneID: "lane-1", Model: canaryValue}
	msgs := []provider.ChatMessage{{Role: "user", Content: "hi"}}
	doors := map[string]func(*Executor) error{
		"Chat": func(e *Executor) error {
			_, err := e.Chat(ctx, sel, provider.ChatRequest{Model: canaryValue, Messages: msgs})
			return err
		},
		"Embed": func(e *Executor) error {
			_, err := e.Embed(ctx, sel, provider.ModelEmbedRequest{Model: canaryValue, Inputs: []string{"hi"}})
			return err
		},
		"Count": func(e *Executor) error {
			_, err := e.Count(ctx, sel, provider.CountRequest{Model: canaryValue, Text: "hi"})
			return err
		},
		"Stream": func(e *Executor) error {
			return e.Stream(ctx, sel, provider.ChatRequest{Model: canaryValue, Messages: msgs}, func(provider.StreamEvent) error { return nil })
		},
	}
	for name, call := range doors {
		t.Run(name, func(t *testing.T) {
			exec, seen := newCanaryExecutor(t)
			if err := call(exec); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := seen.joined(); !strings.Contains(got, canaryTag) || strings.Contains(got, canaryValue) {
				t.Fatalf("%s: the provider saw %q, want the vault tag in place of the model name", name, got)
			}
		})
	}
}

func TestFailoverUsagePersistsBothAttemptsInTheRealStore(t *testing.T) {
	exec, _, _, provs := failoverRig(t)
	store := newUsageTestStore(t)
	exec.SetUsageStore(store)
	provs["lane-1"].chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Usage: provider.Usage{InputTokens: 9}}, errDenied
	}
	provs["lane-2"].chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Usage: provider.Usage{InputTokens: 2, OutputTokens: 1}}, nil
	}
	if _, err := exec.Execute(context.Background(), validReq()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var rows, in int
	if err := store.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(tokens_in), 0) FROM jobs_usage`).Scan(&rows, &in); err != nil {
		t.Fatalf("count jobs_usage: %v", err)
	}
	if rows != 2 || in != 11 {
		t.Fatalf("jobs_usage holds %d rows / %d tokens in, want both attempts (2 rows, 9+2 tokens)", rows, in)
	}
}

// ctxAudit refuses to write once the call's context is cancelled, as a store
// that honours cancellation does.
type ctxAudit struct{ inner *spyAudit }

func (a ctxAudit) Append(ctx context.Context, e audit.Event) (audit.Record, error) {
	if err := ctx.Err(); err != nil {
		return audit.Record{}, err
	}
	return a.inner.Append(ctx, e)
}

func TestRefusalIsAuditedEvenWhenTheCallerCancelled(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	deps.router.laneFn = func(context.Context, provider.Selection) (laneCandidate, RestrictedPermits, error) {
		return remoteLane("lane-x"), RestrictedPermits{}, nil
	}
	cfg.Router, cfg.Audit = deps.router, ctxAudit{inner: deps.audit}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exec.Chat(ctx, provider.Selection{LaneID: "lane-x"}, provider.ChatRequest{})
	wantViolation(t, err, "lane-x", "restricted")
	if deps.audit.count() != 1 {
		t.Fatalf("audit count = %d, want the refusal recorded despite the cancelled caller", deps.audit.count())
	}
}

func TestExecuteTierJoinsTheThreadPrivacyMode(t *testing.T) {
	cfg, _ := newReadyConfig(t)
	pol := &tierPolicy{}
	cfg.Policy = pol
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	req := validReq()
	req.Sensitivity = provider.SensitivityPublic
	ctx := ContextWithThreadPrivacy(context.Background(), ThreadPrivacy{ThreadID: "th", Mode: provider.SensitivityLocalOnly})
	_, _ = exec.Execute(ctx, req)
	if pol.seen != provider.SensitivityLocalOnly {
		t.Fatalf("policy saw %s, want the local-only thread mode joined into a public request", pol.seen)
	}
}

func TestLeafCancellationAuditCarriesTheCause(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	started := make(chan struct{})
	deps.prov.chatFn = func(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	}
	parent, stop := context.WithCancelCause(context.Background())
	go func() {
		<-started
		stop(errors.New("operator shutdown"))
	}()
	pub := ContextWithDoorSensitivity(parent, provider.SensitivityPublic)
	if _, err := exec.Chat(pub, provider.Selection{LaneID: "lane-1"}, provider.ChatRequest{Messages: validReq().Inputs}); err == nil {
		t.Fatal("Chat on a cancelled context returned no error")
	}
	ev := deps.audit.events[len(deps.audit.events)-1]
	if ev.Outcome != "cancellation" || !strings.Contains(string(ev.Explain), "operator shutdown") {
		t.Fatalf("audit outcome %q explain %s, want a cancellation carrying the parent's cause", ev.Outcome, ev.Explain)
	}
}

func TestDoorKeepsAConversationWhoseFirstMessageEndsInASecretShape(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	var seen []provider.ChatMessage
	deps.prov.chatFn = func(_ context.Context, r provider.ChatRequest) (provider.ChatResponse, error) {
		seen = r.Messages
		return provider.ChatResponse{}, nil
	}
	const url = "postgres://app:Hunter2Hunter2xx@db.example.com:5432/app"
	msgs := []provider.ChatMessage{{Role: "user", Content: "connect to " + url}, {Role: "user", Content: "and then list the tables"}}
	pub := ContextWithDoorSensitivity(context.Background(), provider.SensitivityPublic)
	if _, err := exec.Chat(pub, provider.Selection{LaneID: "lane-1"}, provider.ChatRequest{Messages: msgs}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(seen) != 2 || seen[1].Content != "and then list the tables" || strings.Contains(seen[0].Content, "Hunter2Hunter2xx") {
		t.Fatalf("provider saw %+v, want the secret scrubbed from message 1 and message 2 intact", seen)
	}
}

func TestDoorSubstitutesTheSelectionModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, deps := newReadyConfig(t)
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	if cfg.Firewall, err = egress.NewEngine(egress.DefaultRegistry(), canaryVault{}, detector); err != nil {
		t.Fatalf("egress.NewEngine: %v", err)
	}
	res := &selResolver{prov: deps.prov}
	cfg.Router, cfg.Resolver = NewRouter(laneRegistry(), &fakeQuota{}, nil, nil), res
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.Chat(context.Background(), provider.Selection{LaneID: "lane-a", Model: canaryValue}, provider.ChatRequest{Messages: validReq().Inputs}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(res.sels) != 1 || strings.Contains(res.sels[0].Model, canaryValue) || !strings.Contains(res.sels[0].Model, canaryTag) {
		t.Fatalf("the resolver saw %+v, want the vault tag in place of the selection's model name", res.sels)
	}
}

func TestDoorTagsASecretInEveryFieldNotJustTheLastOne(t *testing.T) {
	// A detector that reads past a field's end would shadow the connection
	// string if the fields were joined into one engine call.
	const dsn = "postgres://app:Hunter2Hunter2xx@db.example.com:5432/app"
	const key = "sk-proj0123456789abcdefghijKLMN"
	exec, deps := newReadyExecutor(t)
	var seen []string
	deps.prov.chatFn = func(_ context.Context, r provider.ChatRequest) (provider.ChatResponse, error) {
		seen = chatTexts(r.Messages)
		return provider.ChatResponse{}, nil
	}
	deps.prov.embedFn = func(_ context.Context, r provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
		seen = r.Inputs
		return provider.ModelEmbedResponse{}, nil
	}
	pub := ContextWithDoorSensitivity(context.Background(), provider.SensitivityPublic)
	sel := provider.Selection{LaneID: "lane-1"}
	msgs := []provider.ChatMessage{{Role: "user", Content: "use " + dsn}, {Role: "user", Content: key + " is the openai key"}}
	if _, err := exec.Chat(pub, sel, provider.ChatRequest{Messages: msgs}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := strings.Join(seen, "\n"); len(seen) != 2 || !strings.Contains(got, "<apikey>") || strings.Contains(got, "Hunter2Hunter2xx") || strings.Contains(got, key) {
		t.Fatalf("Chat: the provider saw %q, want both messages with the secrets tagged", got)
	}
	if _, err := exec.Embed(pub, sel, provider.ModelEmbedRequest{Inputs: []string{"use " + dsn, key + " is the key"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if got := strings.Join(seen, "\n"); len(seen) != 2 || !strings.Contains(got, "<apikey>") || strings.Contains(got, "Hunter2Hunter2xx") || strings.Contains(got, key) {
		t.Fatalf("Embed: the provider saw %q, want both inputs with the secrets tagged", got)
	}
}
