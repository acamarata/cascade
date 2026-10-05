// Purpose: TestResolveResumeSettings, the [daemon] fanout_record_ttl and
//   fanout_sweep_interval resolution: defaults, both value forms, a real
//   config.toml through runtime.Load, and every refusal.
// SPORT: internal/daemon (CHANGE, P1-CORE-15).

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestResolveResumeSettings(t *testing.T) {
	daemonSection := func(kv map[string]interface{}) *runtime.Config {
		return &runtime.Config{Extra: map[string]interface{}{"daemon": kv}}
	}
	ok := []struct {
		name     string
		cfg      *runtime.Config
		ttl, int time.Duration
	}{
		{"nil config", nil, 24 * time.Hour, time.Hour},
		{"no daemon section", &runtime.Config{Extra: map[string]interface{}{"other": 1}}, 24 * time.Hour, time.Hour},
		{"daemon section of the wrong type", &runtime.Config{Extra: map[string]interface{}{"daemon": "x"}}, 24 * time.Hour, time.Hour},
		{"duration strings", daemonSection(map[string]interface{}{"fanout_record_ttl": "90m", "fanout_sweep_interval": "5m"}), 90 * time.Minute, 5 * time.Minute},
		{"seconds", daemonSection(map[string]interface{}{"fanout_record_ttl": int64(60), "fanout_sweep_interval": 0.5}), time.Minute, 500 * time.Millisecond},
		{"one set, one default", daemonSection(map[string]interface{}{"fanout_sweep_interval": "2s"}), 24 * time.Hour, 2 * time.Second},
	}
	for _, c := range ok {
		s, err := ResolveResumeSettings(c.cfg)
		if err != nil || s.RecordTTL != c.ttl || s.SweepInterval != c.int {
			t.Errorf("%s: (%+v, %v), want ttl %v interval %v", c.name, s, err, c.ttl, c.int)
		}
	}
	for name, kv := range map[string]map[string]interface{}{
		"unparsable ttl":     {"fanout_record_ttl": "soon"},
		"zero interval":      {"fanout_sweep_interval": "0s"},
		"negative ttl":       {"fanout_record_ttl": int64(-5)},
		"wrong type":         {"fanout_sweep_interval": true},
		"negative float ttl": {"fanout_record_ttl": -1.5},
	} {
		s, err := ResolveResumeSettings(daemonSection(kv))
		if !cascade.HasKind(err, cascade.KindInvalidInput) || s != (ResumeSettings{}) {
			t.Errorf("%s: (%+v, %v), want KindInvalidInput and zero settings", name, s, err)
		}
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[daemon]\nfanout_record_ttl = \"2s\"\nfanout_sweep_interval = 1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := runtime.Load(context.Background(), runtime.LoadOptions{Path: path,
		Getenv: func(string) string { return "" }, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatalf("runtime.Load: %v", err)
	}
	if s, err := ResolveResumeSettings(cfg); err != nil || s.RecordTTL != 2*time.Second || s.SweepInterval != time.Second {
		t.Fatalf("from config.toml: (%+v, %v), want ttl 2s interval 1s", s, err)
	}
}
