package learn

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

func learnRegistry(t *testing.T) (*rpc.Registry, *SQLiteCapabilityScorer) {
	t.Helper()
	s := newTestScorer(t, newStepClock())
	r := rpc.NewRegistry()
	r.SetAccessPolicy(nil)
	if err := RegisterLearnHandlers(r, LearnReadDeps{Scorer: s, Resetter: &SQLiteObservationResetter{DB: s.db}, Estimates: NewSQLiteEstimateSource(s.db), Clock: s.clock}); err != nil {
		t.Fatal(err)
	}
	return r, s
}

func learnCall(t *testing.T, r *rpc.Registry, method, params string, out any) *rpc.ErrorObject {
	t.Helper()
	v, e := r.Dispatch(principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}), &rpc.Request{Method: method, Params: json.RawMessage(params)})
	if e == nil && out != nil {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestLearnRPC_Status_ZeroRows(t *testing.T) {
	r, _ := learnRegistry(t)
	var out struct {
		Scores []ScoreRow `json:"scores"`
	}
	if e := learnCall(t, r, "learn.status", "{}", &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Scores) != 28 {
		t.Fatalf("scores = %d", len(out.Scores))
	}
	for _, s := range out.Scores {
		if s.FallbackLevel != "priors" || s.Observations != 0 || !near(s.Alpha+s.Beta, 4) {
			t.Fatalf("prior = %+v", s)
		}
	}
}

func TestLearnRPC_Status_SeededScores(t *testing.T) {
	r, s := learnRegistry(t)
	seedScore(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierOne, 6, 1, 7, s.clock.Now())
	var out struct {
		Scores []ScoreRow `json:"scores"`
	}
	if e := learnCall(t, r, "learn.status", "{}", &out); e != nil {
		t.Fatal(e)
	}
	for _, row := range out.Scores {
		if row.Tier == string(capacity.TierOne) && row.TaskClass == string(conductor.TaskClassCode) {
			a, b, _ := PriorAlphaBeta(capacity.TierOne, conductor.TaskClassCode)
			if row.Observations != 7 || !near(row.Score, (a+6)/(a+b+7)) || row.FallbackLevel != "global" {
				t.Fatalf("score=%+v", row)
			}
			return
		}
	}
	t.Fatal("seeded score missing")
}

func TestLearnRPC_Explain_KnownPair(t *testing.T) {
	r, s := learnRegistry(t)
	seedScore(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierOne, 6, 1, 7, s.clock.Now())
	var out ExplainResult
	if e := learnCall(t, r, "learn.explain", `{"scope_key":"global","task_class":"code"}`, &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Scores) != 4 || out.Tier0 == nil || out.BestLower == nil || out.Threshold <= 0 {
		t.Fatalf("explanation=%+v", out)
	}
	for _, row := range out.Scores {
		if row.Tier == string(capacity.TierZero) {
			q, duration := NewSQLiteEstimateSource(s.db).Estimate(capacity.TierZero, conductor.TaskClassCode)
			want := capacity.ComputeExpectedTime(q, duration, row.Score)
			if out.Tier0.ExpectedTime != want.ExpectedTime || out.Tier0.PRework != want.PRework || out.Tier0.ReworkCyclesEst != want.ReworkCyclesEst {
				t.Fatalf("estimate=%+v, want %+v", out.Tier0, want)
			}
		}
	}
	if out.Threshold != capacity.JumpHysteresisFactor || out.JumpFires != capacity.JumpFires(capacity.ExpectedTime{ExpectedTime: out.Tier0.ExpectedTime}, capacity.ExpectedTime{ExpectedTime: out.BestLower.ExpectedTime}) {
		t.Fatalf("jump comparison=%+v", out)
	}
}

func TestLearnRPC_Explain_UnknownPairPriors(t *testing.T) {
	r, _ := learnRegistry(t)
	var out ExplainResult
	if e := learnCall(t, r, "learn.explain", `{"scope_key":"repo:unknown","task_class":"code"}`, &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Scores) != 4 {
		t.Fatalf("scores=%+v", out.Scores)
	}
	for _, s := range out.Scores {
		if s.FallbackLevel != "priors" {
			t.Fatalf("unlabelled prior=%+v", s)
		}
	}
}

func TestLearnRPC_Reset_Idempotent(t *testing.T) {
	r, s := learnRegistry(t)
	seedScore(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierOne, 1, 0, 1, s.clock.Now())
	for i := 0; i < 2; i++ {
		var out struct {
			RowsDeleted int  `json:"rows_deleted"`
			Changed     bool `json:"changed"`
		}
		if e := learnCall(t, r, "learn.reset", `{"scope_key":"global"}`, &out); e != nil {
			t.Fatal(e)
		}
		if out.RowsDeleted != 1-i || out.Changed != (i == 0) {
			t.Fatalf("reset %d=%+v", i, out)
		}
	}
}

func TestLearnRPC_Reset_RequiresScopeOrAll(t *testing.T) {
	r, _ := learnRegistry(t)
	for _, p := range []string{`{}`, `{"task_class":"code"}`, `{"tier":"tier-1"}`} {
		if e := learnCall(t, r, "learn.reset", p, nil); e == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	if e := learnCall(t, r, "learn.reset", `{"all":true}`, nil); e != nil {
		t.Fatal(e)
	}
}

func TestLearnRPC_ReadErrorIsNotPriors(t *testing.T) {
	r, s := learnRegistry(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"learn.status", "learn.explain"} {
		if e := learnCall(t, r, m, `{"scope_key":"global","task_class":"code"}`, nil); e == nil {
			t.Fatalf("%s hid read error", m)
		}
	}
}

func TestLearnRPC_OwnerOnly(t *testing.T) {
	r, _ := learnRegistry(t)
	for _, m := range []string{"learn.status", "learn.explain", "learn.reset"} {
		for _, ctx := range []context.Context{context.Background(), principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{Kind: principal.KindSession}})} {
			if _, e := r.Dispatch(ctx, &rpc.Request{Method: m, Params: json.RawMessage(`{}`)}); e == nil {
				t.Fatalf("%s admitted non-owner", m)
			}
		}
	}
}

func TestLearnRPC_InvalidParameters(t *testing.T) {
	r, _ := learnRegistry(t)
	for _, p := range []string{`{`, `{}`, `{"scope_key":"invalid","task_class":"code"}`, `{"scope_key":"global","task_class":"chat"}`} {
		if e := learnCall(t, r, "learn.explain", p, nil); e == nil {
			t.Fatalf("explain accepted %s", p)
		}
	}
	if e := learnCall(t, r, "learn.reset", `{`, nil); e == nil {
		t.Fatal("reset accepted malformed parameters")
	}
	if err := RegisterLearnHandlers(nil, LearnReadDeps{}); err == nil {
		t.Fatal("accepted missing dependencies")
	}
}

func TestLearnRPC_Reset_Filters(t *testing.T) {
	r, s := learnRegistry(t)
	for _, tier := range []capacity.Tier{capacity.TierOne, capacity.TierTwo} {
		seedScore(t, s, scopeGlobal, conductor.TaskClassCode, tier, 1, 0, 1, s.clock.Now())
	}
	if e := learnCall(t, r, "learn.reset", `{"scope_key":"global","task_class":"code","tier":"tier-1"}`, nil); e != nil {
		t.Fatal(e)
	}
	if _, found := readStored(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierOne); found {
		t.Fatal("matching row survived")
	}
	if _, found := readStored(t, s, scopeGlobal, conductor.TaskClassCode, capacity.TierTwo); !found {
		t.Fatal("nonmatching row deleted")
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if e := learnCall(t, r, "learn.reset", `{"all":true}`, nil); e == nil {
		t.Fatal("reset hid write error")
	}
}
