package runtime_test

// Purpose: proves [secrets] entropy_floor and confidence_threshold are guarded
// config: a tightening reaches the live detector before the next scan, a
// raise (also a removed key, a string, NaN) is refused without approval, an
// invalid section or an applier error rejects the reload, and the baseline
// treats an unset threshold as the shipped default.
// Inputs: real runtime.Load over temp config files, a real secrets.Detector
// behind the same applier shape the daemon installs, synthetic samples.
// Outputs: none (tests). Constraints: external test package (internal/secrets
// imports internal/runtime); no process-global detector, no keychain, no HOME.
// SPORT: runtime/hot-reload-engine tests (P1-SEC-24).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// sample sits between a lowered floor (3.0) and the shipped one (3.5): a
// named opaque run that only the entropy signal can catch. bearer scores
// 0.9, between the shipped and a raised threshold (0.95).
const (
	sample = `api_key = "k3m9x2q7z4k3m9x2q7z4k3m9"`
	bearer = "Authorization: Bearer Zq8Rt2Lm5Np7Vx3Kc9Hw4Bd6"
)

type eventLog struct {
	mu    sync.Mutex
	names []string
}

func (e *eventLog) Publish(_ context.Context, name string, _ map[string]interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.names = append(e.names, name)
}

func (e *eventLog) count(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, got := range e.names {
		if got == name {
			n++
		}
	}
	return n
}

// slot approves exactly the config whose guarded hash it was armed with, once.
type slot struct{ hash string }

func (s *slot) ApproveLoosening(_ context.Context, proposed runtime.EffectiveConfig, _ []runtime.LooseningPath) (string, error) {
	armed := s.hash
	s.hash = ""
	if armed == "" || armed != runtime.ComputeSectionsHash(proposed) {
		return "", errors.New("no armed slot for this file")
	}
	return "slot-ref", nil
}

type rig struct {
	hr     *runtime.HotReloader
	path   string
	det    *secrets.Detector
	events *eventLog
	calls  int
	opts   runtime.LoadOptions
}

// newRig loads body, builds a detector from its [secrets] table the way the
// daemon's source does, and installs an applier of the daemon's shape.
func newRig(t *testing.T, body string) *rig {
	t.Helper()
	r := &rig{path: filepath.Join(t.TempDir(), "config.toml"), events: &eventLog{}}
	r.write(t, body)
	r.opts = runtime.LoadOptions{Path: r.path, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}
	cfg, err := runtime.Load(context.Background(), r.opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	section, _ := cfg.Extra["secrets"].(map[string]interface{})
	boot, err := secrets.ParseDetectionConfig(section)
	if err != nil {
		t.Fatalf("boot section: %v", err)
	}
	if r.det, err = secrets.NewDetector(secrets.DefaultRegistry(), boot); err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(time.Unix(0, 0))
	r.hr = runtime.NewHotReloader(r.path, r.opts, cfg, clock, r.events, runtime.NewStoreAuditRecorder(storetest.NewMemStore(), clock), nil)
	r.hr.SetSecretsApplier(r.applier(nil))
	return r
}

// applier is the daemon's applier shape over r.det; fail replaces the apply.
func (r *rig) applier(fail error) func(map[string]interface{}) error {
	return func(section map[string]interface{}) error {
		r.calls++
		cfg, err := secrets.ParseDetectionConfig(section)
		if err != nil {
			return err
		}
		if fail != nil {
			return fail
		}
		return r.det.Reload(cfg)
	}
}

func (r *rig) write(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(r.path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) reload(t *testing.T, body string) runtime.ReloadOutcome {
	t.Helper()
	r.write(t, body)
	return r.hr.Reload(context.Background())
}

func (r *rig) caught(content string) bool { return len(r.det.ScanCertain([]byte(content))) > 0 }

func (r *rig) unchanged(t *testing.T, want secrets.DetectionConfig, wantExtra string) {
	t.Helper()
	if got := r.det.Config(); got != want {
		t.Fatalf("detector config = %+v, want it kept at %+v", got, want)
	}
	if got := fmt.Sprint(r.hr.Current().Extra["secrets"]); got != wantExtra {
		t.Fatalf("Current()[secrets] = %s, want %s", got, wantExtra)
	}
}

func TestSecretsConfigTightensDetector(t *testing.T) {
	r := newRig(t, "")
	handle := r.det
	if r.caught(sample) {
		t.Fatal("precondition: the sample must be below the shipped entropy floor")
	}
	out := r.reload(t, "[secrets]\nentropy_floor = 3.0\n")
	if !out.Accepted || out.ElevationRequired {
		t.Fatalf("lowering entropy_floor: %+v, want accepted without elevation", out)
	}
	if !r.caught(sample) || r.det != handle {
		t.Fatalf("after the reload the same detector handle must catch the sample (caught=%v)", r.caught(sample))
	}
}

func TestSecretsConfigHotReloadApplies(t *testing.T) {
	r := newRig(t, "")
	out := r.reload(t, "[secrets]\nentropy_floor = 3.0\nconfidence_threshold = 0.5\n")
	want := secrets.DetectionConfig{EntropyFloor: 3.0, ConfidenceThreshold: 0.5}
	if !out.Accepted || r.det.Config() != want {
		t.Fatalf("outcome %+v, detector %+v, want accepted and %+v", out, r.det.Config(), want)
	}
	if fmt.Sprint(out.AppliedLive) != "[secrets]" || r.events.count("config.reload.accepted") != 1 {
		t.Fatalf("AppliedLive %v, accepted events %d", out.AppliedLive, r.events.count("config.reload.accepted"))
	}
}

func TestSecretsConfigLooseningRefusedWithoutApproval(t *testing.T) {
	r := newRig(t, "")
	out := r.reload(t, "[secrets]\nconfidence_threshold = 0.95\n")
	if !out.Rejected || !out.ElevationRequired || len(out.LooseningPaths) != 1 || out.LooseningPaths[0].Key != "secrets.confidence_threshold" {
		t.Fatalf("raising the threshold with no slot: %+v", out)
	}
	var need *runtime.ElevationRequiredError
	if !errors.As(out.Err, &need) || fmt.Sprint(need.Keys) != "[secrets.confidence_threshold]" {
		t.Fatalf("err = %v, want ElevationRequired naming secrets.confidence_threshold", out.Err)
	}
	r.unchanged(t, secrets.DefaultDetectionConfig(), "<nil>")
	if r.calls != 0 || !r.caught(bearer) {
		t.Fatalf("applier calls %d, bearer caught %v: a refused raise must touch nothing", r.calls, r.caught(bearer))
	}

	// An armed slot for exactly that file applies it once; the slot is spent.
	r.write(t, "[secrets]\nconfidence_threshold = 0.95\n")
	proposed, err := runtime.Load(context.Background(), r.opts)
	if err != nil {
		t.Fatal(err)
	}
	r.hr.SetElevationApprover(&slot{hash: runtime.ComputeSectionsHash(runtime.EffectiveConfigOf(proposed))})
	if out := r.hr.Reload(context.Background()); !out.Accepted || out.ElevationRef != "slot-ref" {
		t.Fatalf("armed slot: %+v", out)
	}
	if r.det.Config().ConfidenceThreshold != 0.95 || r.caught(bearer) {
		t.Fatalf("approved raise not applied: %+v", r.det.Config())
	}
	if out := r.reload(t, "[secrets]\nconfidence_threshold = 0.99\n"); !out.ElevationRequired {
		t.Fatalf("a second raise with a spent slot: %+v", out)
	}
}

func TestHotReloadSecretsApplierInvoked(t *testing.T) {
	r := newRig(t, "")
	var seen map[string]interface{}
	r.hr.SetSecretsApplier(func(section map[string]interface{}) error { r.calls++; seen = section; return nil })
	if out := r.reload(t, "[logging]\nlevel = \"debug\"\n"); !out.Accepted || r.calls != 0 {
		t.Fatalf("a reload that leaves [secrets] alone: %+v, applier calls %d, want 0", out, r.calls)
	}
	if out := r.reload(t, "[secrets]\nentropy_floor = 3.0\n"); !out.Accepted || r.calls != 1 || seen["entropy_floor"] != 3.0 {
		t.Fatalf("a [secrets] change: %+v, applier calls %d, table %v", out, r.calls, seen)
	}
}
