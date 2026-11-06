package learn

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeReader returns fixed Get results.
type fakeReader struct {
	raw     string
	present bool
	source  runtime.ConfigSource
	err     error
}

func (f fakeReader) Get(string) (string, bool, runtime.ConfigSource, string, error) {
	return f.raw, f.present, f.source, "digest", f.err
}

func TestDiffTier_SelfDescribedSafeGateTableIsSecurity(t *testing.T) {
	isolateHome(t)
	r := RuntimeConfigReader{Path: filepath.Join(t.TempDir(), "config.toml")}
	gate := LearnedConfig{Target: "mandatory.security_gates", Tier: TierSafe,
		Change: Change{Op: OpSet, Path: "ci.policy.require_tests", Value: "false"}}
	d := DiffTier(r, gate)
	if d.Computed.Tier != TierSecurity || d.Stored != TierSafe || !d.Mismatch {
		t.Fatalf("gate table: %+v", d)
	}
	if !slices.Contains(d.Computed.Areas, AreaMandatorySecurityGates) {
		t.Fatalf("gate areas: %v", d.Computed.Areas)
	}

	control := LearnedConfig{Target: "ci.local_timeout", Tier: TierSafe,
		Change: Change{Op: OpSet, Path: "ci.local.timeout_seconds", Value: "120"}}
	d = DiffTier(r, control)
	if d.Computed.Tier != TierSafe || d.Mismatch || d.Held || d.Materialised != "120" {
		t.Fatalf("control: %+v", d)
	}
}

func TestDiffTierUnresolvableIsSecurity(t *testing.T) {
	nope := LearnedConfig{Target: "nope.target", Tier: TierSafe,
		Change: Change{Op: OpSet, Path: "ci.local.timeout_seconds", Value: "120"}}
	d := DiffTier(fakeReader{}, nope)
	if d.Computed.Tier != TierSecurity || !d.Held || d.HoldReason != HoldUnmaterialisable || !d.Mismatch {
		t.Fatalf("unknown target: %+v", d)
	}
	requireKindMsg(t, d.Err, cascade.KindNotFound, "unknown learned-config target")

	lanes := fakeReader{raw: `["a", "b"]`, present: true, source: runtime.SourceFile}
	reorder := LearnedConfig{Target: "conductor.lane_order", Tier: TierSafe,
		Change: Change{Op: OpReorder, Path: "conductor.lane_order", Value: `["a","c"]`}}
	d = DiffTier(lanes, reorder)
	if d.Computed.Tier != TierSecurity || d.HoldReason != HoldUnmaterialisable || !d.Mismatch {
		t.Fatalf("reorder to a different set: %+v", d)
	}
	reorder.Change.Value = `["b","a"]`
	d = DiffTier(lanes, reorder)
	if d.Computed.Tier != TierSafe || d.Held || d.Mismatch || d.Materialised != `["b","a"]` {
		t.Fatalf("reorder of the same set: %+v", d)
	}

	readErr := cascade.New(cascade.KindUnavailable, "synthetic read failure")
	d = DiffTier(fakeReader{err: readErr}, reorder)
	if d.Computed.Tier != TierSecurity || d.HoldReason != HoldReadError || d.Err != readErr {
		t.Fatalf("reader error: %+v", d)
	}

	d = DiffTier(nil, reorder)
	if d.Computed.Tier != TierSecurity || d.HoldReason != HoldReadError || !d.Held {
		t.Fatalf("nil reader: %+v", d)
	}
	requireKindMsg(t, d.Err, cascade.KindUnavailable, "no config reader")
}

func TestDiffTierAddStepUnknownValueIsSecurity(t *testing.T) {
	list := fakeReader{raw: `["go build ./..."]`, present: true, source: runtime.SourceFile}
	c := LearnedConfig{Target: "ci.local_steps", Tier: TierBehavioral,
		Change: Change{Op: OpAddStep, Path: "ci.local.build", Value: `"make deploy"`}}
	d := DiffTier(list, c)
	if d.Computed.Tier != TierSecurity || !d.Computed.LoosensBound || !d.Mismatch {
		t.Fatalf("unknown step: %+v", d)
	}

	c.Change.Value = `"cascade context generate --check"`
	d = DiffTier(list, c)
	if d.Computed.Tier != TierBehavioral || d.Mismatch || d.Held {
		t.Fatalf("known step: %+v", d)
	}
}
