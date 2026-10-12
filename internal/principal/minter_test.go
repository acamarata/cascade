package principal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestMintRefusesUnmintableKinds(t *testing.T) {
	c := runtime.NewFixedClock(time.Unix(100, 0))
	m := NewMinter(c)
	for _, k := range []Kind{"node", KindPlugin, "", "unlisted"} {
		before, _, _ := Status(m)
		token, err := m.Mint(Principal{ID: "test", Kind: k, Scope: "project:test", EnvelopeDigest: "digest"}, time.Hour)
		kind, _ := cascade.KindOf(err)
		after, _, _ := Status(m)
		if token != "" || kind != cascade.KindInvalidInput || after != before {
			t.Fatalf("kind %q minted=%t: %v %d/%d", k, token != "", err, before, after)
		}
	}
	for _, k := range []Kind{KindSession, KindMCP, KindHook, KindNodeJob, KindRemoteClient} {
		token, err := m.Mint(Principal{ID: string(k), Kind: k, Scope: "project:test", EnvelopeDigest: "digest"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		p, err := m.Resolve(token, c.Now())
		if err != nil || p.Kind != k {
			t.Fatal(p, err)
		}
	}
	if n, _, _ := Status(m); n != 5 {
		t.Fatal(n)
	}
}

func TestPrincipalTokenRefusals(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(100, 0))
	m := NewMinter(clock)
	store := storetest.NewMemStore()
	m.Audit = runtime.NewStoreAuditRecorder(store, clock)
	p := Principal{ID: "caller", Kind: KindSession, Scope: "project:test"}
	token, err := m.Mint(p, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	for _, value := range []string{token, "cpt1_" + strings.Repeat("A", 43)} {
		_, err = m.Resolve(value, clock.Now())
		if err != errUnrecognized || err.(*cascade.Error).Msg != "caller principal not recognized" {
			t.Fatal(err)
		}
	}
	m.Revoke(p.ID)
	_, err = m.Resolve(token, clock.Now())
	if err != errUnrecognized {
		t.Fatal(err)
	}
	if live, _, _ := Status(m); live != 0 {
		t.Fatal(live)
	}
	if fresh, err := m.Mint(p, time.Hour); err == nil || fresh != "" {
		t.Fatal("revoked id reissued")
	}
	other := NewMinter(clock)
	if _, err := other.Resolve(token, clock.Now()); err != errUnrecognized {
		t.Fatal(err)
	}
	rows, err := store.Scan(context.Background(), "audit", "caller_principal_refused/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	reasons := map[string]bool{}
	for rows.Next(context.Background()) {
		var rec struct {
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(rows.Value(), &rec); err != nil {
			t.Fatal(err)
		}
		reasons[rec.Fields["reason"]] = true
	}
	if rows.Err() != nil || len(reasons) != 3 {
		t.Fatal(reasons, rows.Err())
	}
}

func TestCallerContextAndBinding(t *testing.T) {
	if _, ok := CallerFrom(context.Background()); ok {
		t.Fatal("anonymous context became owner")
	}
	p := Principal{ID: "caller", Kind: KindMCP, Scope: "project:test"}
	ctx := WithCaller(context.Background(), Caller{Principal: &p, OwnerCLI: true})
	c, ok := CallerFrom(ctx)
	if !ok || c.OwnerCLI || c.Principal == nil || c.Principal.ID != p.ID {
		t.Fatal(c, ok)
	}
	owner, ok := CallerFrom(WithCaller(context.Background(), Caller{OwnerCLI: true}))
	if !ok || !owner.OwnerCLI || owner.Principal != nil {
		t.Fatal(owner, ok)
	}
	env := BindEnv([]string{"OTHER=kept", EnvToken + "=inherited", EnvToken + "=duplicate"}, "replacement")
	if len(env) != 2 || env[0] != "OTHER=kept" || env[1] != EnvToken+"=replacement" {
		t.Fatal(env)
	}
}

func TestMintRequiresProjectScope(t *testing.T) {
	m := NewMinter(runtime.NewFixedClock(time.Unix(100, 0)))
	for _, scope := range []string{"", "personal", "global", "*", "global:root", "personal:owner", "project:", "project: x", "project:a b", "project:a\x00b", "project:" + strings.Repeat("x", 505), "PROJECT:x", "project:*", "workspace:x"} {
		if token, err := m.Mint(Principal{ID: "s", Kind: KindSession, Scope: scope}, time.Hour); err == nil || token != "" {
			t.Errorf("scope %q minted %q", scope, token)
		} else if e, ok := err.(*cascade.Error); !ok || e.Kind != cascade.KindInvalidInput {
			t.Errorf("scope %q: %v", scope, err)
		}
	}
	for _, scope := range []string{"project:test", "project:org:repo", "project:" + strings.Repeat("x", 504)} {
		if token, err := m.Mint(Principal{ID: "s", Kind: KindSession, Scope: scope}, time.Hour); err != nil || token == "" {
			t.Errorf("scope %q refused: %v", scope, err)
		}
	}
}
