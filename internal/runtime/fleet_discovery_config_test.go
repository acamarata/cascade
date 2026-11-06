package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// assertFleetDiscoveryErr checks err by identity, kind and message: the
// sentinel's own text plus the offending raw value must both appear,
// because errors.Is on a taxonomy error compares Kind only.
func assertFleetDiscoveryErr(t *testing.T, err error, raw string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want ErrFleetDiscoveryInterval for %s, got nil", raw)
	}
	if !errors.Is(err, ErrFleetDiscoveryInterval) {
		t.Fatalf("errors.Is(%v, ErrFleetDiscoveryInterval) = false", err)
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("kind = %v (ok=%v), want KindInvalidInput", k, ok)
	}
	msg := err.Error()
	if !strings.Contains(msg, ErrFleetDiscoveryInterval.Msg) {
		t.Fatalf("message %q lacks the sentinel text %q", msg, ErrFleetDiscoveryInterval.Msg)
	}
	if !strings.Contains(msg, raw) {
		t.Fatalf("message %q does not name the value %s", msg, raw)
	}
}

func TestFleetDiscoveryConfigKey(t *testing.T) {
	const key = "fleet.discovery.interval"
	if _, err := ResolveDottedPath(key); err != nil {
		t.Fatalf("ResolveDottedPath(%q): %v", key, err)
	}
	if !slices.Contains(KnownConfigKeys(), key) {
		t.Fatalf("KnownConfigKeys() lacks %q", key)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	w := &ConfigWriter{Path: path}
	if _, err := w.Set(key, `"30m"`); err != nil {
		assertFleetDiscoveryErr(t, err, "30m")
	} else {
		t.Fatal("Set 30m succeeded, want a refusal")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused Set touched disk: stat err = %v", err)
	}

	if _, err := w.Set(key, `"1h"`); err != nil {
		t.Fatalf("Set 1h: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), `interval = "1h"`) {
		t.Fatalf("config lacks interval = \"1h\":\n%s", data)
	}
	tree, err := DecodeConfigFile(path)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	fleet, _ := tree["fleet"].(map[string]interface{})
	disc, _ := fleet["discovery"].(map[string]interface{})
	if disc["interval"] != "1h" {
		t.Fatalf("decoded interval = %v, want 1h", disc["interval"])
	}
}

func fleetTree(interval interface{}) map[string]interface{} {
	return map[string]interface{}{
		"fleet": map[string]interface{}{
			"discovery": map[string]interface{}{"interval": interval},
		},
	}
}

func TestFleetDiscoveryValidateRefusesBadValues(t *testing.T) {
	bad := []struct {
		name string
		tree map[string]interface{}
		raw  string
	}{
		{"30m", fleetTree("30m"), `"30m"`},
		{"59m59s", fleetTree("59m59s"), `"59m59s"`},
		{"abc", fleetTree("abc"), `"abc"`},
		{"integer", fleetTree(int64(3600)), "3600"},
		{"non-table discovery", map[string]interface{}{
			"fleet": map[string]interface{}{"discovery": "6h"},
		}, `"6h"`},
	}
	for _, c := range bad {
		t.Run("refuses "+c.name, func(t *testing.T) {
			assertFleetDiscoveryErr(t, Validate(c.tree), c.raw)
		})
	}

	good := map[string]map[string]interface{}{
		"1h":       fleetTree("1h"),
		"6h":       fleetTree("6h"),
		"no fleet": {"logging": map[string]interface{}{"level": "info"}},
	}
	for name, tree := range good {
		t.Run("accepts "+name, func(t *testing.T) {
			if err := Validate(tree); err != nil {
				t.Fatalf("Validate(%s) = %v, want nil", name, err)
			}
		})
	}
}

func TestFleetDiscoveryIntervalDefault(t *testing.T) {
	if FleetDiscoveryIntervalDefault != 6*time.Hour || FleetDiscoveryIntervalMin != time.Hour {
		t.Fatalf("default/min = %v/%v, want 6h/1h", FleetDiscoveryIntervalDefault, FleetDiscoveryIntervalMin)
	}
	for name, cfg := range map[string]*Config{
		"nil":          nil,
		"no discovery": {Extra: map[string]interface{}{"fleet": map[string]interface{}{}}},
		"empty extra":  {},
	} {
		got, err := FleetDiscoveryInterval(cfg)
		if err != nil || got != 6*time.Hour {
			t.Fatalf("%s: got %v, %v; want 6h, nil", name, got, err)
		}
	}

	got, err := FleetDiscoveryInterval(&Config{Extra: fleetTree("2h")})
	if err != nil || got != 2*time.Hour {
		t.Fatalf("2h: got %v, %v; want 2h, nil", got, err)
	}

	got, err = FleetDiscoveryInterval(&Config{Extra: fleetTree("30m")})
	assertFleetDiscoveryErr(t, err, `"30m"`)
	if got != 0 {
		t.Fatalf("refused value returned %v, want 0", got)
	}
}

func TestFleetDiscoveryIntervalIsHot(t *testing.T) {
	if slices.Contains(coldSections, "fleet") {
		t.Fatal(`"fleet" is in coldSections; the interval must be hot`)
	}
	old := &Config{Extra: fleetTree("6h")}
	proposed := &Config{Extra: fleetTree("2h")}
	if keys := coldKeyDiff(old, proposed); len(keys) != 0 {
		t.Fatalf("coldKeyDiff over an interval change = %v, want none", keys)
	}
	// Paired positive: the same diff does report a real cold key.
	proposed.Extra["daemon"] = map[string]interface{}{"socket": "/x.sock"}
	if keys := coldKeyDiff(old, proposed); !slices.Contains(keys, "daemon.socket") {
		t.Fatalf("coldKeyDiff missed daemon.socket: %v", keys)
	}
}
