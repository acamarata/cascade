// Purpose: tests for the spawn effect (intent before the driver call, confirm
// after, a crash between them), the job id and filter the shim sets, the tool
// policy and lease marker refusals, and the conductor import boundary.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-03.

package agents

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const effectKey = "agent.spawn:job-fix-1"

func TestShimSpawnEffectIntentConfirm(t *testing.T) {
	f := newShimFix(t)
	var during audit.EffectPhase
	var found bool
	f.drv.before = func(provider.AgentJobSpec) {
		var err error
		if during, found, err = f.el.EffectState(context.Background(), effectKey); err != nil {
			t.Errorf("EffectState during Spawn: %v", err)
		}
	}
	h, err := f.shim.Spawn(context.Background(), f.req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = h.Release(context.Background()) })
	if !found || during != audit.EffectIntent {
		t.Fatalf("effect state when the driver ran = %q (found %v), want the intent committed first", during, found)
	}
	after, found, err := f.el.EffectState(context.Background(), effectKey)
	if err != nil || !found || after != audit.EffectConfirmed {
		t.Fatalf("effect state after Spawn = %q, %v, %v; want confirmed", after, found, err)
	}
}

func TestShimSpawnFailureFailsTheIntent(t *testing.T) {
	f := newShimFix(t)
	f.drv.spawnErr = cascade.New(cascade.KindUnavailable, "driver refused")
	_, err := f.shim.Spawn(context.Background(), f.req)
	if err == nil || !strings.Contains(err.Error(), "driver refused") {
		t.Fatalf("Spawn = %v, want the driver's refusal", err)
	}
	st, found, serr := f.el.EffectState(context.Background(), effectKey)
	if serr != nil || !found || st != audit.EffectFailed {
		t.Fatalf("effect state = %q, %v, %v; want failed", st, found, serr)
	}
	if d := f.profileDirs(); len(d) != 0 {
		t.Fatalf("profile left after a driver failure: %v", d)
	}
}

// diesBeforeConfirm models a child that exits after Spawn and takes the host
// with it: the confirm never lands.
type diesBeforeConfirm struct{ audit.EffectWriter }

func (diesBeforeConfirm) ConfirmEffect(context.Context, audit.EffectHandle, string) (audit.Record, error) {
	return audit.Record{}, errors.New("synthetic crash before confirm")
}

func TestShimSpawnCrashLeavesUnknownOutcome(t *testing.T) {
	f := newShimFix(t)
	f.deps.Effects = diesBeforeConfirm{f.el}
	f.build()
	if _, err := f.shim.Spawn(context.Background(), f.req); err == nil {
		t.Fatal("Spawn succeeded although the confirm never landed")
	}
	ctx := context.Background()
	pending, err := f.el.PendingEffects(ctx)
	if err != nil || len(pending) != 1 || pending[0].Key != effectKey {
		t.Fatalf("PendingEffects = %v, %v; want the one spawn intent", pending, err)
	}
	if _, err := f.el.MarkUnknownOutcome(ctx, pending[0]); err != nil {
		t.Fatalf("MarkUnknownOutcome: %v", err)
	}
	if st, _, _ := f.el.EffectState(ctx, effectKey); st != audit.EffectUnknown {
		t.Fatalf("recovered state = %q, want unknown-outcome", st)
	}
	f.deps.Effects = f.el
	f.build()
	_, err = f.shim.Spawn(ctx, f.req)
	if err == nil || !strings.Contains(err.Error(), "already begun") {
		t.Fatalf("second Spawn = %v, want the already-begun refusal", err)
	}
	if n := f.drv.spawned(); n != 1 {
		t.Fatalf("driver spawned %d times, want exactly once (never repeated)", n)
	}
}

var mintedJobID = regexp.MustCompile(`^agt-[0-9a-f]{16}$`)

func TestSpawnSetsJobIDAndFilter(t *testing.T) {
	f := newShimFix(t)
	f.req.Job.JobID = ""
	h, err := f.shim.Spawn(context.Background(), f.req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = h.Release(context.Background()) })
	spec := f.drv.specs[0]
	if !mintedJobID.MatchString(string(spec.JobID)) || h.JobID != spec.JobID {
		t.Fatalf("spec job id %q, handle %q; want a minted id on both", spec.JobID, h.JobID)
	}
	if spec.Filter == nil {
		t.Fatal("spec.Filter is nil")
	}
	out, err := spec.Filter([]byte("echo " + shimKeyValue + " done"))
	if err != nil || strings.Contains(string(out), shimKeyValue) || !strings.Contains(string(out), RedactedTag) {
		t.Fatalf("filter output %q, %v; want the key scrubbed", out, err)
	}
}

// TestSpawnFilterTierIsTheJoin: the filter runs at the more restrictive of the
// envelope's tier and the job's data class, so neither can lower the other.
func TestSpawnFilterTierIsTheJoin(t *testing.T) {
	cases := map[string]struct {
		env, job provider.SensitivityTier
		admitted bool
	}{
		"internal and internal":           {provider.SensitivityInternal, provider.SensitivityInternal, true},
		"envelope public, job internal":   {provider.SensitivityPublic, provider.SensitivityInternal, true},
		"envelope restricted, job public": {provider.SensitivityRestricted, provider.SensitivityPublic, false},
		"envelope internal, job unset":    {provider.SensitivityInternal, provider.SensitivityRestricted, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newShimFix(t)
			f.req.Job.Envelope.Sensitivity, f.req.Job.DataClass = tc.env, tc.job
			h, err := f.shim.Spawn(context.Background(), f.req)
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			t.Cleanup(func() { _ = h.Release(context.Background()) })
			out, ferr := f.drv.specs[0].Filter([]byte("hello"))
			if tc.admitted != (ferr == nil) {
				t.Fatalf("filter = %q, %v; admitted want %v", out, ferr, tc.admitted)
			}
			if !tc.admitted && (out != nil || ferr != provider.ErrEgressSubstitutionFailed) { //nolint:errorlint // identity
				t.Fatalf("refused filter = %q, %v; want nil and ErrEgressSubstitutionFailed", out, ferr)
			}
		})
	}
}

func TestShimRefusesToolPolicyWiderThanEnvelope(t *testing.T) {
	f := newShimFix(t)
	f.req.Job.Envelope.Tools = []string{"agent.tools.readonly"}
	f.req.Job.ToolPolicy = provider.ToolPolicyAll
	_, err := f.shim.Spawn(context.Background(), f.req)
	f.refused(err)
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "tool policy") {
		t.Fatalf("Spawn = %v, want KindPolicyDenied: tool policy", err)
	}
	for _, ok := range []struct {
		tools  []string
		policy string
	}{
		{[]string{"agent.tools.readonly"}, provider.ToolPolicyReadonly},
		{[]string{"agent.tools.all"}, provider.ToolPolicyAll},
		{nil, ""},
	} {
		g := newShimFix(t)
		g.req.Job.Envelope.Tools, g.req.Job.ToolPolicy = ok.tools, ok.policy
		h, gerr := g.shim.Spawn(context.Background(), g.req)
		if gerr != nil {
			t.Fatalf("policy %q with tools %v refused: %v", ok.policy, ok.tools, gerr)
		}
		_ = h.Release(context.Background())
	}
	if got := ToolPolicyFromEnvelope([]string{"agent.tools.bogus", "shell"}); got != provider.ToolPolicyNone {
		t.Fatalf("ToolPolicyFromEnvelope(bogus) = %q, want none", got)
	}
}

func TestShimRefusesEmptyLeaseMarker(t *testing.T) {
	for _, marker := range []string{"", "clm_short", "plain"} {
		f := newShimFix(t)
		f.req.LeaseMarker = marker
		_, err := f.shim.Spawn(context.Background(), f.req)
		f.refused(err)
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "lease marker") {
			t.Fatalf("marker %q: Spawn = %v, want KindInvalidInput naming the lease marker", marker, err)
		}
	}
}

func TestNewShimRefusesIncompleteDeps(t *testing.T) {
	f := newShimFix(t)
	for name, mutate := range map[string]func(d *ShimDeps){
		"no resolver":     func(d *ShimDeps) { d.Resolver = nil },
		"no engine":       func(d *ShimDeps) { d.Engine = nil },
		"no clock":        func(d *ShimDeps) { d.Clock = nil },
		"relative exe":    func(d *ShimDeps) { d.Exe = "cascade" },
		"relative base":   func(d *ShimDeps) { d.ProfileBase = "profiles" },
		"relative vendor": func(d *ShimDeps) { d.Binaries = map[HarnessID]string{HarnessClaude: "claude"} },
	} {
		d := f.deps
		mutate(&d)
		if _, err := NewShim(d); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: NewShim = %v, want KindInvalidInput", name, err)
		}
	}
}

func TestShimImportsNoConductor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		file, perr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, "/internal/conductor") {
				t.Errorf("%s imports %s; the shim must not import the conductor", name, imp.Path.Value)
			}
		}
	}
}

func TestShimRemovesProfileOnRefusal(t *testing.T) {
	cases := map[string]func(f *shimFix){
		"unlisted executable": func(f *shimFix) { f.drv.exe = filepath.Join(t.TempDir(), "claude") },
		"credential read":     func(f *shimFix) { delete(f.env, shimKeyEnv) },
		"engine unavailable": func(f *shimFix) {
			f.deps.Engine = func() (*egress.Engine, error) { return nil, cascade.New(cascade.KindUnavailable, "no firewall") }
		},
		"driver factory": func(f *shimFix) {
			f.useFactory(func(SpawnSite) (provider.AgentProvider, error) {
				return nil, cascade.New(cascade.KindUnavailable, "factory refused")
			})
		},
		"effect intent": func(f *shimFix) { f.deps.Effects = failingEffects{f.el} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newShimFix(t)
			mutate(f)
			f.build()
			_, err := f.shim.Spawn(context.Background(), f.req)
			f.refused(err)
		})
	}
}

// useFactory swaps the claude row's driver factory.
func (f *shimFix) useFactory(fn DriverFactory) {
	f.t.Helper()
	reg := &registry{rows: map[HarnessID]entry{}}
	reg.add(claudeRow, fn)
	res, err := newResolver(reg, func(string) bool { return true })
	if err != nil {
		f.t.Fatal(err)
	}
	f.deps.Resolver = res
}

type failingEffects struct{ audit.EffectWriter }

func (failingEffects) BeginEffect(context.Context, audit.EffectRequest) (audit.EffectHandle, error) {
	return audit.EffectHandle{}, errors.New("synthetic begin failure")
}

func TestShimReleaseRemovesProfile(t *testing.T) {
	f := newShimFix(t)
	h, err := f.shim.Spawn(context.Background(), f.req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if len(f.profileDirs()) != 1 || h.PGID != 4242 || h.JobID != "job-fix-1" || h.Driver == nil {
		t.Fatalf("handle %+v, profiles %v: want one live profile and pgid 4242", h, f.profileDirs())
	}
	for i := 0; i < 2; i++ {
		if err := h.Release(context.Background()); err != nil {
			t.Fatalf("Release #%d = %v, want nil", i, err)
		}
	}
	if d := f.profileDirs(); len(d) != 0 {
		t.Fatalf("profile left after Release: %v", d)
	}
	if f.drv.cancels != 1 || len(f.swept) != 1 || f.swept[0] != f.req.LeaseMarker {
		t.Fatalf("Release cancelled %d times and swept %v; want one cancel, one sweep of the marker", f.drv.cancels, f.swept)
	}
}
