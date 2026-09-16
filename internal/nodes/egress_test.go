package nodes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

// Purpose (this file): the §D-30 registration this package owns, and the
//   dispatch-stream red-team rule — no vaulted value in ANY dispatch
//   stream.
// Note on placement: the S-37.T2 contract names
//   internal/secrets/red_team_test.go for the red-team cases. No such file
//   exists; the tree's red-team suite is internal/hooks/egress/red_team_test.go,
//   and the dispatch-stream cases belong with the code that builds those
//   streams rather than in either. Recorded in the ticket journal.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// TestEgressClassNodeDispatch_InterceptConfigRegistered proves linking this
// package registers the node-dispatch class on the shared registry, which
// is what makes every dispatch payload transit the substitution and
// sensitivity pass rather than bypassing it.
func TestEgressClassNodeDispatch_InterceptConfigRegistered(t *testing.T) {
	cfg, ok := egress.DefaultRegistry().Lookup(egress.EgressClassNodeDispatch)
	if !ok {
		t.Fatal("the node-dispatch egress class is not registered; dispatch payloads would bypass the pass")
	}
	if !cfg.Enabled {
		t.Error("the node-dispatch class is registered but disabled, so nothing intercepts its payloads")
	}
	if cfg.Owner == "" {
		t.Error("the registration names no owner")
	}
}

// TestNoVaultedValueRidesADispatchStream is the red-team rule across every
// outbound surface this ticket owns: the shipped payload, the journal
// stream, and the credential plan. The assertion is that the value is
// REFUSED or absent, never merely redacted downstream — a dispatch stream
// is written to git and to a durable journal, so a value that reaches it is
// a value at rest.
func TestNoVaultedValueRidesADispatchStream(t *testing.T) {
	const vaulted = "sk-rtt-AAAA1234567890BBBBcc"

	// 1. The ship leg refuses a payload carrying it.
	if err := AssertNoStaticKey("d1", []byte(`{"env":{"OPENAI_API_KEY":"`+vaulted+`"}}`)); err == nil {
		t.Error("a payload carrying a vaulted value was admitted to a dispatch")
	}

	// 2. The journal leg refuses the same bytes before the store.
	deps, sink, attempt := streamHarness(t)
	rec := goodRecord(attempt)
	rec.Payload = json.RawMessage(`{"token":"` + vaulted + `"}`)
	if err := StreamJournalRecord(context.Background(), deps, rec); err == nil {
		t.Error("a journal record carrying a vaulted value was appended")
	}
	for _, p := range sink.payloads {
		if strings.Contains(string(p), vaulted) {
			t.Fatalf("a vaulted value reached the journal store: %s", p)
		}
	}

	// 3. A scoped grant carries the vault KEY, never the value: the grant
	// is journalled and logged, so a field holding material would put it
	// at rest even though nothing "sent" it.
	grant, err := NewTokenGrant("job-1", "n1", "api.example", "OPENAI_API_KEY",
		[]string{"chat"}, time.Now(), MaxTokenLifetime)
	if err != nil {
		t.Fatalf("NewTokenGrant: %v", err)
	}
	rendered, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), vaulted) {
		t.Fatalf("a per-dispatch grant carries secret material: %s", rendered)
	}
}
