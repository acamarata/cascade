package runtime_test

// Purpose: the [secrets] threshold loosening table (every route to a higher
// value needs approval), invalid-section rejection, applier failure and
// reload serialisation. Rig and helpers: hotreload_secrets_test.go.
// SPORT: runtime/hot-reload-engine tests (P1-SEC-24).

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestSecretsConfigLooseningTable: every route to a higher threshold needs
// approval, including the ones that do not look like a raise.
func TestSecretsConfigLooseningTable(t *testing.T) {
	tight := "[secrets]\nentropy_floor = 3.0\nconfidence_threshold = 0.5\n"
	cases := []struct{ name, from, to, key string }{
		{"removed tightened entropy key", tight, "[secrets]\nconfidence_threshold = 0.5\n", "secrets.entropy_floor"},
		{"removed tightened threshold key", tight, "[secrets]\nentropy_floor = 3.0\n", "secrets.confidence_threshold"},
		{"removed whole section", tight, "", "secrets.entropy_floor"},
		{"raise to default value", tight, "[secrets]\nentropy_floor = 3.5\nconfidence_threshold = 0.5\n", "secrets.entropy_floor"},
		{"raise as integer", "", "[secrets]\nentropy_floor = 4\n", "secrets.entropy_floor"},
		{"string for a number", "", "[secrets]\nconfidence_threshold = \"0.8\"\n", "secrets.confidence_threshold"},
		{"string below current", tight, "[secrets]\nentropy_floor = \"1\"\nconfidence_threshold = 0.5\n", "secrets.entropy_floor"},
		{"nan", tight, "[secrets]\nentropy_floor = nan\nconfidence_threshold = 0.5\n", "secrets.entropy_floor"},
		{"inf", "", "[secrets]\nentropy_floor = inf\n", "secrets.entropy_floor"},
		{"boolean", "", "[secrets]\nconfidence_threshold = true\n", "secrets.confidence_threshold"},
		{"zero threshold", "", "[secrets]\nconfidence_threshold = 0\n", "secrets.confidence_threshold"},
		{"negative floor", "", "[secrets]\nentropy_floor = -1\n", "secrets.entropy_floor"},
		{"threshold above one", "", "[secrets]\nconfidence_threshold = 1.5\n", "secrets.confidence_threshold"},
		{"floor above eight", "", "[secrets]\nentropy_floor = 9.0\n", "secrets.entropy_floor"},
		{"negative below a tightened floor", tight, "[secrets]\nentropy_floor = -1\nconfidence_threshold = 0.5\n", "secrets.entropy_floor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.from)
			before, extra := r.det.Config(), fmt.Sprint(r.hr.Current().Extra["secrets"])
			out := r.reload(t, tc.to)
			if !out.Rejected || !out.ElevationRequired {
				t.Fatalf("%+v, want a refusal for want of elevation", out)
			}
			named := false
			for _, p := range out.LooseningPaths {
				named = named || p.Key == tc.key
			}
			if !named {
				t.Fatalf("loosening paths %+v do not name %s", out.LooseningPaths, tc.key)
			}
			r.unchanged(t, before, extra)
			if r.calls != 0 {
				t.Fatalf("applier called %d times for a refused raise", r.calls)
			}
		})
	}
}

// approveAll stands in for an armed slot, so a case reaches the applier.
type approveAll struct{}

func (approveAll) ApproveLoosening(context.Context, runtime.EffectiveConfig, []runtime.LooseningPath) (string, error) {
	return "approved", nil
}

// TestSecretsReloadInvalidKeepsPrevious: every invalid [secrets] is rejected
// with the previous config and detector kept, at BOTH layers. Without an
// approver the guard refuses it (an invalid value is never a tightening).
// With every loosening approved the case must still reach the applier, and
// the applier refuses it: each body keeps entropy_floor = 3.0 where it can, so
// nothing but the invalid part stops it first.
func TestSecretsReloadInvalidKeepsPrevious(t *testing.T) {
	const base = "[secrets]\nentropy_floor = 3.0\n"
	cases := map[string]string{
		"zero threshold":      base + "confidence_threshold = 0\n",
		"negative floor":      "[secrets]\nentropy_floor = -1\n",
		"floor above eight":   "[secrets]\nentropy_floor = 9.0\n",
		"threshold above one": base + "confidence_threshold = 1.5\n",
		"typo'd key":          base + "confidence_treshold = 0.9\n",
		"string threshold":    base + "confidence_threshold = \"0.8\"\n",
		"nan floor":           "[secrets]\nentropy_floor = nan\n",
		"boolean threshold":   base + "confidence_threshold = true\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, base)
			before, extra := r.det.Config(), fmt.Sprint(r.hr.Current().Extra["secrets"])
			out := r.reload(t, body)
			if !out.Rejected || out.Accepted {
				t.Fatalf("guard layer: %+v, want rejected", out)
			}
			r.unchanged(t, before, extra)

			r.hr.SetElevationApprover(approveAll{})
			calls := r.calls
			out = r.reload(t, body)
			if !out.Rejected || out.Accepted || r.calls != calls+1 || !cascade.HasKind(out.Err, cascade.KindInvalidInput) {
				t.Fatalf("applier layer: %+v, applier calls %d, want the applier reached and refusing", out, r.calls-calls)
			}
			r.unchanged(t, before, extra)
			if r.events.count("config.reload.accepted") != 0 {
				t.Fatalf("events %v", r.events.names)
			}
		})
	}
	t.Run("secrets not a table", func(t *testing.T) {
		r := newRig(t, "")
		out := r.reload(t, "secrets = \"on\"\n")
		if !out.Rejected || out.ElevationRequired || !cascade.HasKind(out.Err, cascade.KindInvalidInput) || r.calls != 0 {
			t.Fatalf("%+v, applier calls %d, want the not-a-table branch (KindInvalidInput, no elevation, applier not reached)", out, r.calls)
		}
		r.unchanged(t, secrets.DefaultDetectionConfig(), "<nil>")
	})
}

// TestSecretsReloadApplierErrorKeepsPreviousConfig: an applier that fails
// after validation (custody gone) leaves config and detector as they were.
func TestSecretsReloadApplierErrorKeepsPreviousConfig(t *testing.T) {
	r := newRig(t, "")
	r.hr.SetSecretsApplier(r.applier(cascade.New(cascade.KindUnavailable, "custody unavailable")))
	out := r.reload(t, "[secrets]\nentropy_floor = 3.0\n")
	if !out.Rejected || !cascade.HasKind(out.Err, cascade.KindUnavailable) || r.calls != 1 {
		t.Fatalf("%+v, applier calls %d", out, r.calls)
	}
	r.unchanged(t, secrets.DefaultDetectionConfig(), "<nil>")
}

// TestSecretsReloadsAreSerialised: two reloads cannot be inside the applier
// at once, so the detector always equals what Current() declares. Every
// loosening is approved so each distinct file reaches the applier. The second
// entry is made certain rather than hoped for: the first applier call waits
// until every goroutine has renamed its file, then replaces the file with a
// value none of them wrote, so each reload still queued loads a table that
// differs from the one just stored and must apply again.
func TestSecretsReloadsAreSerialised(t *testing.T) {
	const workers = 8
	r := newRig(t, "")
	r.hr.SetElevationApprover(approveAll{})
	var renamed sync.WaitGroup
	var inflight, overlaps, entered atomic.Int32
	r.hr.SetSecretsApplier(serialisedApplier(t, r, &renamed, &inflight, &overlaps, &entered))
	var wg sync.WaitGroup
	start := make(chan struct{})
	renamed.Add(workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			replaceFile(t, fmt.Sprintf("%s.%d", r.path, i), r.path, fmt.Sprintf("[secrets]\nentropy_floor = 2.%d\n", i))
			renamed.Done()
			// No Reload starts until every rename is done: on Windows a rename
			// over a file another Reload has open fails.
			renamed.Wait()
			r.hr.Reload(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()
	if entered.Load() < 2 {
		t.Fatalf("the applier was entered %d times; the test is built to enter it at least twice", entered.Load())
	}
	if overlaps.Load() != 0 {
		t.Fatalf("the applier was entered concurrently %d times", overlaps.Load())
	}
	section, _ := r.hr.Current().Extra["secrets"].(map[string]interface{})
	if section["entropy_floor"] != 1.5 || r.det.Config().EntropyFloor != 1.5 {
		t.Fatalf("Current() %v and detector %v, want both at the last written 1.5", section, r.det.Config().EntropyFloor)
	}
}

// replaceFile writes body to tmp and renames it over dst, so a reader sees
// either file whole.
func replaceFile(t *testing.T, tmp, dst, body string) {
	t.Helper()
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		t.Error(err)
	}
}

// serialisedApplier counts entries and overlaps. Its first call waits for every
// worker to have renamed its file, then writes a value none of them wrote.
func serialisedApplier(t *testing.T, r *rig, renamed *sync.WaitGroup, inflight, overlaps, entered *atomic.Int32) func(map[string]interface{}) error {
	var once sync.Once
	return func(section map[string]interface{}) error {
		entered.Add(1)
		if inflight.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer inflight.Add(-1)
		once.Do(func() {
			renamed.Wait()
			replaceFile(t, r.path+".final", r.path, "[secrets]\nentropy_floor = 1.5\n")
		})
		time.Sleep(10 * time.Millisecond) // long enough for the others to arrive
		cfg, err := secrets.ParseDetectionConfig(section)
		if err != nil {
			return err
		}
		return r.det.Reload(cfg)
	}
}

// TestSecretsReloadRetriesAfterAFailedApply: after an applier failure the
// next reload applies again even when the file is back to what is running,
// so a subsystem the failure marked degraded can heal.
func TestSecretsReloadRetriesAfterAFailedApply(t *testing.T) {
	r := newRig(t, "")
	r.hr.SetSecretsApplier(r.applier(cascade.New(cascade.KindUnavailable, "custody unavailable")))
	if out := r.reload(t, "[secrets]\nentropy_floor = 3.0\n"); !out.Rejected || r.calls != 1 {
		t.Fatalf("failing apply: %+v, calls %d", out, r.calls)
	}
	r.hr.SetSecretsApplier(r.applier(nil))
	if out := r.reload(t, ""); !out.Accepted || r.calls != 2 {
		t.Fatalf("revert after a failure: %+v, applier calls %d, want the applier to run again", out, r.calls)
	}
	if out := r.reload(t, ""); !out.Accepted || r.calls != 2 {
		t.Fatalf("a further unchanged reload: %+v, applier calls %d, want no retry once healed", out, r.calls)
	}
}
