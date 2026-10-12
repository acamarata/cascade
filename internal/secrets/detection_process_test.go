package secrets

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the process detector's contract. One pointer per
// process; a source error is sticky and never replaced by the defaults; a
// rejected configuration changes nothing; a source arrives once and only
// before the first resolution.
// Constraints: the state is package-global, so every test snapshots it and
// restores it. No I/O.

// isolateProcessDetection clears the process detector for one test and
// restores what was there afterwards.
func isolateProcessDetection(t *testing.T) {
	t.Helper()
	processDetection.mu.Lock()
	src, det, err := processDetection.src, processDetection.detector, processDetection.err
	processDetection.src, processDetection.detector, processDetection.err = nil, nil, nil
	processDetection.mu.Unlock()
	t.Cleanup(func() {
		processDetection.mu.Lock()
		processDetection.src, processDetection.detector, processDetection.err = src, det, err
		processDetection.mu.Unlock()
	})
}

func TestProcessDetectorSingleton(t *testing.T) {
	isolateProcessDetection(t)
	want := DetectionConfig{EntropyFloor: 3.0, ConfidenceThreshold: 0.95}
	if err := SetProcessDetectionConfigSource(func() (DetectionConfig, error) { return want, nil }); err != nil {
		t.Fatal(err)
	}
	first, err := ProcessDetector()
	if err != nil {
		t.Fatalf("ProcessDetector: %v", err)
	}
	second, err := ProcessDetector()
	if err != nil || second != first {
		t.Fatalf("second call = (%p, %v), want the same pointer %p", second, err, first)
	}
	if got := first.Config(); got != want {
		t.Fatalf("config = %+v, want the source's %+v", got, want)
	}
	if got := ProcessDetectionConfig(); got != want {
		t.Fatalf("ProcessDetectionConfig = %+v, want %+v", got, want)
	}
	reloaded := DetectionConfig{EntropyFloor: 4.0, ConfidenceThreshold: 0.9}
	if err := SetProcessDetectionConfig(reloaded); err != nil {
		t.Fatal(err)
	}
	third, _ := ProcessDetector()
	if third != first || first.Config() != reloaded {
		t.Fatalf("SetProcessDetectionConfig replaced the detector (%p vs %p) or did not apply (%+v)", third, first, first.Config())
	}
}

func TestProcessDetectorDefaultsWithoutSource(t *testing.T) {
	isolateProcessDetection(t)
	det, err := ProcessDetector()
	if err != nil {
		t.Fatal(err)
	}
	if det.Config() != DefaultDetectionConfig() {
		t.Fatalf("config = %+v, want the shipped defaults", det.Config())
	}
	// A source arriving after the defaults were resolved would leave the
	// first scan sites on rules the operator did not set.
	err = SetProcessDetectionConfigSource(func() (DetectionConfig, error) { return DefaultDetectionConfig(), nil })
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("late source: err = %v, want KindConflict", err)
	}
}

func TestSetProcessDetectionConfigSourceOnceAndNotNil(t *testing.T) {
	isolateProcessDetection(t)
	if err := SetProcessDetectionConfigSource(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil source: err = %v, want KindInvalidInput", err)
	}
	src := func() (DetectionConfig, error) { return DefaultDetectionConfig(), nil }
	if err := SetProcessDetectionConfigSource(src); err != nil {
		t.Fatal(err)
	}
	if err := SetProcessDetectionConfigSource(src); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second source: err = %v, want KindConflict", err)
	}
}

func TestProcessDetectorSourceErrorIsSticky(t *testing.T) {
	isolateProcessDetection(t)
	calls := 0
	_, cause := ParseDetectionConfig(map[string]interface{}{"confidence_threshold": "0.8"})
	if cause == nil {
		t.Fatal("ParseDetectionConfig accepted a string threshold")
	}
	if err := SetProcessDetectionConfigSource(func() (DetectionConfig, error) { calls++; return DetectionConfig{}, cause }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		det, err := ProcessDetector()
		if det != nil || err != cause { //nolint:errorlint // identity: the source error is returned unchanged
			t.Fatalf("call %d = (%p, %v), want (nil, the source error)", i, det, err)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "confidence_threshold") {
			t.Fatalf("err = %v, want KindInvalidInput naming the key", err)
		}
	}
	if calls != 1 {
		t.Fatalf("the source ran %d times; a failure is sticky, not retried", calls)
	}
	if got := ProcessDetectionConfig(); got.Validate() == nil {
		t.Fatalf("ProcessDetectionConfig on a failed detector = %+v, a valid config; want one Validate refuses", got)
	}
	ok := DetectionConfig{EntropyFloor: 3.5, ConfidenceThreshold: 0.95}
	if err := SetProcessDetectionConfig(ok); err != nil {
		t.Fatal(err)
	}
	det, err := ProcessDetector()
	if err != nil || det.Config() != ok {
		t.Fatalf("after SetProcessDetectionConfig: (%v, %v), want the installed config", det, err)
	}
}

func TestSetProcessDetectionConfigRefusesInvalid(t *testing.T) {
	isolateProcessDetection(t)
	det, err := ProcessDetector()
	if err != nil {
		t.Fatal(err)
	}
	before := det.Config()
	for _, bad := range []DetectionConfig{
		{EntropyFloor: 3.5, ConfidenceThreshold: 0},
		{EntropyFloor: 3.5, ConfidenceThreshold: 1.5},
		{EntropyFloor: -1, ConfidenceThreshold: 0.8},
		{EntropyFloor: 9, ConfidenceThreshold: 0.8},
	} {
		if err := SetProcessDetectionConfig(bad); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("SetProcessDetectionConfig(%+v) = %v, want KindInvalidInput", bad, err)
		}
		if got := det.Config(); got != before {
			t.Fatalf("a refused config changed the detector: %+v -> %+v", before, got)
		}
		if again, _ := ProcessDetector(); again != det {
			t.Fatal("a refused config replaced the detector")
		}
	}
}

func TestSetProcessDetectionConfigKeepsStickyErrorOnRefusal(t *testing.T) {
	isolateProcessDetection(t)
	cause := cascade.New(cascade.KindInvalidInput, "test: bad [secrets]")
	if err := SetProcessDetectionConfigSource(func() (DetectionConfig, error) { return DetectionConfig{}, cause }); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessDetector(); err != cause { //nolint:errorlint // identity
		t.Fatalf("err = %v, want the source error", err)
	}
	if err := SetProcessDetectionConfig(DetectionConfig{}); err == nil {
		t.Fatal("SetProcessDetectionConfig accepted the zero config")
	}
	if _, err := ProcessDetector(); err != cause { //nolint:errorlint // identity
		t.Fatalf("a refused SetProcessDetectionConfig cleared the source error: %v", err)
	}
}

// TestParseDetectionConfigRefusesUnknownKey: a key [secrets] does not
// define is refused and named (a typo must not silently keep the shipped
// threshold); the keys other readers own are accepted; a key that is not
// key-shaped is refused without being echoed.
func TestParseDetectionConfigRefusesUnknownKey(t *testing.T) {
	cases := []struct {
		name    string
		section map[string]interface{}
		wantErr string // "" = accepted
	}{
		{"typo", map[string]interface{}{"confidence_treshold": 0.95}, `"confidence_treshold"`},
		{"owned elsewhere", map[string]interface{}{"keychain_backend": "os", "clipboard_ttl": int64(30)}, ""},
		{"not key-shaped", map[string]interface{}{"Plain-Canary-9f!": 1.0}, "unrecognised key"},
		{"long run, no underscore", map[string]interface{}{strings.Repeat("ab", 17): 1.0}, "unrecognised key"},
		{"detector flags it", map[string]interface{}{"gh" + "p_" + strings.Repeat("q7", 12): 1.0}, "unrecognised key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseDetectionConfig(tc.section)
			if tc.wantErr == "" {
				if err != nil || cfg != DefaultDetectionConfig() {
					t.Fatalf("ParseDetectionConfig = (%+v, %v), want the defaults", cfg, err)
				}
				return
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseDetectionConfig = %v, want KindInvalidInput containing %s", err, tc.wantErr)
			}
			for key := range tc.section {
				if tc.wantErr == "unrecognised key" && strings.Contains(err.Error(), key) {
					t.Fatalf("a key that may be a value was echoed: %v", err)
				}
			}
		})
	}
}
