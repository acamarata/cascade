package memory

// Purpose: memory.consolidate's decoder, its result shape, its
//   registration on the real router, and the [memory] config parser both
//   it and the scheduled jobs are configured from.
// Constraints: every store lives under t.TempDir(); the frozen clock
//   supplies every instant; nothing here reaches the network.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newAdmin returns an AdminHandler over a fresh fixture.
func newAdmin(t *testing.T) (*AdminHandler, *consolidationFixture) {
	t.Helper()
	f := newConsolidationFixture(t)
	return NewAdminHandler(f.c, ConsolidationConfig{}), f
}

// decodeReport pulls the ConsolidationReport out of a handler result.
func decodeReport(t *testing.T, v any) ConsolidationReport {
	t.Helper()
	report, ok := v.(ConsolidationReport)
	if !ok {
		t.Fatalf("result is %T, want ConsolidationReport", v)
	}
	return report
}

// TestMemoryConsolidateRPC runs the verb end to end over a real tree.
func TestMemoryConsolidateRPC(t *testing.T) {
	ctx := context.Background()
	h, f := newAdmin(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)

	got, err := h.Consolidate(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	report := decodeReport(t, got)
	if report.Merged != 1 || report.Retired != 1 || report.DryRun {
		t.Fatalf("report = %+v, want one real merge", report)
	}
}

// TestMemoryConsolidateRPCDryRun proves the flag reaches the job.
func TestMemoryConsolidateRPCDryRun(t *testing.T) {
	ctx := context.Background()
	h, f := newAdmin(t)
	f.write(t, "first", "same\n", "a", KindProject)
	f.write(t, "second", "same\n", "b", KindProject)
	before := treeSnapshot(t, f.base)

	got, err := h.Consolidate(ctx, json.RawMessage(`{"dry_run":true}`))
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	report := decodeReport(t, got)
	if !report.DryRun || report.Retired != 0 || len(report.Groups) != 1 {
		t.Fatalf("report = %+v, want a rehearsal describing one group", report)
	}
	assertTreeUnchanged(t, before, treeSnapshot(t, f.base))
}

// TestMemoryConsolidateRPCAbsentParams proves the verb is callable with no
// params member at all, since every field is optional.
func TestMemoryConsolidateRPCAbsentParams(t *testing.T) {
	h, _ := newAdmin(t)
	for _, params := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`  `)} {
		if _, err := h.Consolidate(context.Background(), params); err != nil {
			t.Errorf("Consolidate(%q): %v", params, err)
		}
	}
}

// TestMemoryConsolidateRPCMalformedParams proves a bad request is a typed
// KindInvalidInput refusal, not a panic and not a silent default.
func TestMemoryConsolidateRPCMalformedParams(t *testing.T) {
	h, _ := newAdmin(t)
	_, err := h.Consolidate(context.Background(), json.RawMessage(`{"dry_run":`))
	if err == nil {
		t.Fatal("malformed params were accepted")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
	if !strings.Contains(err.Error(), MethodConsolidate) {
		t.Errorf("the refusal does not name the method: %v", err)
	}
}

// TestMemoryConsolidateRPCPropagatesRefusals proves a job-level refusal
// reaches the caller rather than being reported as an empty success.
func TestMemoryConsolidateRPCPropagatesRefusals(t *testing.T) {
	f := newConsolidationFixture(t)
	h := NewAdminHandler(f.c, ConsolidationConfig{EmbeddingEnabled: true})
	_, err := h.Consolidate(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, ErrEmbeddingConsolidationUnavailable) {
		t.Fatalf("err = %v, want ErrEmbeddingConsolidationUnavailable", err)
	}
}

// TestMemoryConsolidateIsRegistered is the reachability proof: the method
// must be bound on a real registry, or the verb ships unreachable.
func TestMemoryConsolidateIsRegistered(t *testing.T) {
	h, _ := newAdmin(t)
	registry := rpc.NewRegistry()
	h.Register(registry)
	if !registry.Registered(MethodConsolidate) {
		t.Fatalf("%s is not registered on the router", MethodConsolidate)
	}
}

// TestParseJobConfigDefaults proves an absent or empty section resolves to
// the documented defaults rather than to zero values.
func TestParseJobConfigDefaults(t *testing.T) {
	for name, raw := range map[string]any{
		"absent": nil,
		"empty":  map[string]any{},
	} {
		got, err := ParseJobConfig(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.ConsolidationSchedule != DefaultConsolidationSchedule ||
			got.StalenessSchedule != DefaultStalenessSchedule ||
			got.Staleness.Window != DefaultStalenessWindow ||
			got.Consolidation.EmbeddingEnabled {
			t.Errorf("%s: JobConfig = %+v, want the shipped defaults", name, got)
		}
	}
}

// TestParseJobConfigReadsEveryKey proves each key it owns takes effect.
func TestParseJobConfigReadsEveryKey(t *testing.T) {
	got, err := ParseJobConfig(map[string]any{
		ConfigKeyConsolidationSchedule:  "@every 1h0m0s",
		ConfigKeyStalenessSchedule:      "0 3 * * *",
		ConfigKeyStalenessWindowDays:    int64(7),
		ConfigKeyConsolidationEmbedding: true,
		"review_cadence":                "weekly",
	})
	if err != nil {
		t.Fatalf("ParseJobConfig: %v", err)
	}
	if got.ConsolidationSchedule != "@every 1h0m0s" || got.StalenessSchedule != "0 3 * * *" {
		t.Errorf("schedules = %q / %q", got.ConsolidationSchedule, got.StalenessSchedule)
	}
	if got.Staleness.Window != 7*24*time.Hour {
		t.Errorf("Window = %v, want 7 days", got.Staleness.Window)
	}
	if !got.Consolidation.EmbeddingEnabled {
		t.Error("consolidation_embedding did not take effect")
	}
}

// TestParseJobConfigAcceptsFloatAndIntDays proves both shapes a TOML
// decoder can produce for a number are read the same way.
func TestParseJobConfigAcceptsFloatAndIntDays(t *testing.T) {
	for name, value := range map[string]any{"int": 1, "int64": int64(1), "float": 1.0} {
		got, err := ParseJobConfig(map[string]any{ConfigKeyStalenessWindowDays: value})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Staleness.Window != 24*time.Hour {
			t.Errorf("%s: Window = %v, want one day", name, got.Staleness.Window)
		}
	}
}

// TestParseJobConfigRefusesWrongTypes proves a misconfigured key is a hard
// refusal: a user who wrote a window must not be told a different one ran.
func TestParseJobConfigRefusesWrongTypes(t *testing.T) {
	cases := map[string]any{
		"section not a table": "nope",
		"schedule not string": map[string]any{ConfigKeyConsolidationSchedule: 5},
		"schedule empty":      map[string]any{ConfigKeyStalenessSchedule: ""},
		"days not a number":   map[string]any{ConfigKeyStalenessWindowDays: "30"},
		"days not positive":   map[string]any{ConfigKeyStalenessWindowDays: int64(0)},
		"flag not a bool":     map[string]any{ConfigKeyConsolidationEmbedding: "yes"},
	}
	for name, raw := range cases {
		got, err := ParseJobConfig(raw)
		if err == nil {
			t.Errorf("%s: accepted, want a refusal", name)
			continue
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("%s: kind = %v (ok=%v), want KindInvalidInput", name, kind, ok)
		}
		if got.ConsolidationSchedule != DefaultConsolidationSchedule {
			t.Errorf("%s: a refusal returned a half-applied config: %+v", name, got)
		}
	}
}

// FuzzConsolidateRPCParams drives memory.consolidate's external-input
// decoder. The property is not merely "no panic": every input must either
// decode to params the handler accepts, or be refused with a taxonomy
// error, with no third outcome.
func FuzzConsolidateRPCParams(f *testing.F) {
	seedDir := filepath.Join("..", "testdata", "fuzz", "FuzzConsolidateRPCParams")
	entries, err := os.ReadDir(seedDir)
	if err != nil {
		f.Fatalf("reading the seed corpus: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(seedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), readErr)
		}
		f.Add(data)
	}
	f.Add([]byte(nil))
	f.Add([]byte(`{"dry_run":true}`))
	f.Add([]byte(`{"dry_run":"not a bool"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var p ConsolidateParams
		err := decodeParams(MethodConsolidate, json.RawMessage(data), &p)
		if err == nil {
			return
		}
		if _, ok := cascade.KindOf(err); !ok {
			t.Fatalf("a refusal was not a taxonomy error: %v", err)
		}
	})
}
