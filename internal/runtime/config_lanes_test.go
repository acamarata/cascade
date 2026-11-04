package runtime_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const lanePinFile = `[fleet.lanes."lane-a"]
lane_class = "worker-fast"
reversal_until = "2026-12-01T00:00:00Z"
reversal_reason = "temporary capacity recovery"
`

// TestLaneReversalPinParse exercises loaded file pins and fail-closed refusals.
func TestLaneReversalPinParse(t *testing.T) {
	assertValidLanePin(t)
	testLanePinFailures(t)
	testLanePinAbsence(t)
}

func assertValidLanePin(t *testing.T) {
	t.Helper()
	cfg, err := loadLanePinFile(t, lanePinFile, nil)
	pins, err := runtime.ParseLaneReversalPins[topology.LaneClass](cfg, err)
	if err != nil || len(pins) != 1 {
		t.Fatalf("pins = %+v, err = %v", pins, err)
	}
	pin := pins[0]
	until := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if pin.LaneID != "lane-a" || pin.LaneClass != topology.LaneClassWorkerFast ||
		!pin.ReversalUntil.Equal(until) || pin.ReversalReason != "temporary capacity recovery" {
		t.Fatalf("pin = %+v", pin)
	}
}

func testLanePinFailures(t *testing.T) {
	cases := []struct{ name, text, key, message string }{
		{"unknown class", strings.ReplaceAll(lanePinFile, "worker-fast", "not-a-class"), "lane_class", "unknown lane class"},
		{"derived class", strings.ReplaceAll(lanePinFile, "worker-fast", "executive-overflow"), "lane_class", "unknown lane class"},
		{"missing class", strings.ReplaceAll(lanePinFile, "lane_class = \"worker-fast\"\n", ""), "lane_class", "non-empty string"},
		{"missing until", strings.ReplaceAll(lanePinFile, "reversal_until = \"2026-12-01T00:00:00Z\"\n", ""), "reversal_until", "non-empty string"},
		{"bad until", strings.ReplaceAll(lanePinFile, "2026-12-01T00:00:00Z", "tomorrow"), "reversal_until", "UTC RFC3339"},
		{"non UTC", strings.ReplaceAll(lanePinFile, "2026-12-01T00:00:00Z", "2026-12-01T01:00:00+01:00"), "reversal_until", "UTC RFC3339"},
		{"empty reason", strings.ReplaceAll(lanePinFile, "temporary capacity recovery", ""), "reversal_reason", "non-empty string"},
		{"blank reason", strings.ReplaceAll(lanePinFile, "temporary capacity recovery", "  "), "reversal_reason", "non-empty string"},
		{"missing reason", strings.ReplaceAll(lanePinFile, "reversal_reason = \"temporary capacity recovery\"\n", ""), "reversal_reason", "non-empty string"},
		{"wrong class type", strings.ReplaceAll(lanePinFile, `"worker-fast"`, "42"), "lane_class", "non-empty string"},
		{"wrong until type", strings.ReplaceAll(lanePinFile, `"2026-12-01T00:00:00Z"`, "42"), "reversal_until", "non-empty string"},
		{"wrong reason type", strings.ReplaceAll(lanePinFile, `"temporary capacity recovery"`, "false"), "reversal_reason", "non-empty string"},
		{"fleet shape", "fleet = 42\n", "", "must be a table"},
		{"lanes shape", "[fleet]\nlanes = 42\n", "", "must be a table"},
		{"entry shape", "[fleet.lanes]\nlane-a = 42\n", "", "must be a table"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertValidLanePin(t)
			cfg, err := loadLanePinFile(t, tc.text, nil)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			pins, err := runtime.ParseLaneReversalPins[topology.LaneClass](cfg, nil)
			assertLanePinInvalid(t, err, "fleet.lanes", tc.message)
			if len(pins) != 0 || (tc.key != "" && !strings.Contains(err.Error(), "fleet.lanes.lane-a."+tc.key)) {
				t.Fatalf("pins = %+v, err = %v; want no pins and key %s", pins, err, tc.key)
			}
		})
	}
	testLanePinReadFailures(t)
	testLanePinEnvOverrides(t)
}

func testLanePinAbsence(t *testing.T) {
	for _, text := range []string{"", "[fleet]\n", "[fleet.lanes]\n", "[fleet.accounts]\n"} {
		cfg, err := loadLanePinFile(t, text, nil)
		pins, err := runtime.ParseLaneReversalPins[topology.LaneClass](cfg, err)
		if err != nil || len(pins) != 0 {
			t.Fatalf("absent pins: %+v, %v", pins, err)
		}
	}
}

func testLanePinReadFailures(t *testing.T) {
	assertValidLanePin(t)
	_, err := runtime.ParseLaneReversalPins[topology.LaneClass](nil, nil)
	assertLanePinInvalid(t, err, "fleet.lanes", "no configuration")
	cfg, loadErr := loadLanePinFile(t, "[not valid TOML", nil)
	if loadErr == nil {
		t.Fatal("malformed file loaded successfully")
	}
	_, err = runtime.ParseLaneReversalPins[topology.LaneClass](cfg, loadErr)
	assertLanePinInvalid(t, err, "fleet.lanes", "cannot read configuration")
	if errors.Unwrap(err) != loadErr {
		t.Fatalf("lost parse error identity: %v", err)
	}
	cfg, loadErr = loadLanePinFile(t, lanePinFile, nil)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	readErr := errors.New("injected configuration read failure")
	pins, err := runtime.ParseLaneReversalPins[topology.LaneClass](cfg, readErr)
	assertLanePinInvalid(t, err, "fleet.lanes", readErr.Error())
	if len(pins) != 0 || errors.Unwrap(err) != readErr {
		t.Fatalf("read failure: pins = %+v, err = %v", pins, err)
	}
}

func testLanePinEnvOverrides(t *testing.T) {
	for _, field := range []string{"lane_class", "reversal_until", "reversal_reason"} {
		assertValidLanePin(t)
		env := "CASCADE_FLEET__LANES__LANE-A__" + strings.ToUpper(field) + `="worker-fast"`
		cfg, err := loadLanePinFile(t, lanePinFile, []string{env})
		pins, err := runtime.ParseLaneReversalPins[topology.LaneClass](cfg, err)
		assertLanePinInvalid(t, err, "fleet.lanes.lane-a."+field, "file only")
		if len(pins) != 0 {
			t.Fatalf("env override produced pins: %+v", pins)
		}
	}
	for _, env := range []string{
		"CASCADE_FLEET__LANES={}",
		"CASCADE_FLEET__LANES__LANE-A={}",
		`CASCADE_FLEET__LANES={lane-a={lane_class="worker-fast", reversal_until="2026-12-01T00:00:00Z", reversal_reason="env pin"}}`,
	} {
		assertValidLanePin(t)
		cfg, err := loadLanePinFile(t, lanePinFile, []string{env})
		pins, err := runtime.ParseLaneReversalPins[topology.LaneClass](cfg, err)
		assertLanePinInvalid(t, err, "fleet.lanes", "file only")
		if len(pins) != 0 {
			t.Fatalf("parent env override produced pins: %+v", pins)
		}
	}
}

func assertLanePinInvalid(t *testing.T, err error, key, message string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.ErrInvalidInput.Kind ||
		!errors.Is(err, cascade.ErrInvalidInput) || !strings.Contains(err.Error(), key) ||
		!strings.Contains(err.Error(), message) {
		t.Fatalf("err = %v; want InvalidInput naming %s and %q", err, key, message)
	}
	if ce.Err != cascade.ErrInvalidInput && !strings.Contains(ce.Msg, "cannot read configuration") {
		t.Fatalf("missing sentinel identity: %v", err)
	}
}

// TestLaneReversalPinHasNoSetPath checks the resolver and actual writer refuse pins.
func TestLaneReversalPinHasNoSetPath(t *testing.T) {
	cfg, err := loadLanePinFile(t, lanePinFile, nil)
	if _, err := runtime.ParseLaneReversalPins[topology.LaneClass](cfg, err); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(lanePinFile), 0o600); err != nil {
		t.Fatal(err)
	}
	writer := &runtime.ConfigWriter{Path: path}
	for _, key := range []string{"fleet", "fleet.lanes", "fleet.lanes.lane-a", "fleet.lanes.lane-a.lane_class",
		"fleet.lanes.lane-a.reversal_until", "fleet.lanes.lane-a.reversal_reason"} {
		if _, err := runtime.ResolveDottedPath(key); err == nil {
			t.Fatalf("registered set path: %s", key)
		}
		_, err := writer.Set(key, `"worker-fast"`)
		var pathErr *runtime.DottedPathError
		if !errors.As(err, &pathErr) || pathErr.Path != key || pathErr.Reason != "unknown config key" {
			t.Fatalf("Set(%s): %v", key, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != lanePinFile {
		t.Fatalf("refused writes changed config: %q, %v", data, err)
	}
	if _, err := writer.Set("logging.level", `"debug"`); err != nil {
		t.Fatalf("positive set control: %v", err)
	}
}

func loadLanePinFile(t *testing.T, text string, environ []string) (*runtime.Config, error) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return runtime.Load(context.Background(), runtime.LoadOptions{
		Path: path, Getenv: func(string) string { return "" },
		Environ: func() []string { return environ }, Warn: func(string, ...interface{}) {},
	})
}
