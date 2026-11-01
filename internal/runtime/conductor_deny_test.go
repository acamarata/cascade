package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): [conductor].deny_task_classes is a known key the
//   config writer accepts, and ConductorDenyTaskClasses decodes it exactly:
//   absent is the empty list, every malformed shape is KindInvalidInput
//   naming the key, and a nil config is unreadable rather than empty.
// SPORT: runtime/conductor-deny tests (ADD, P1-CAP-07).

// TestConductorDenyTaskClassesKey: the key is in the writer's registry, and
// `config set conductor.deny_task_classes '["code"]'` (ConfigWriter.Set,
// the verb's own write path) in a fresh HOME writes a value Load reads
// back through the decoder.
func TestConductorDenyTaskClassesKey(t *testing.T) {
	if !slices.Contains(knownConfigKeys, "conductor.deny_task_classes") {
		t.Fatal("knownConfigKeys has no conductor.deny_task_classes; `config set` refuses it as unknown")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".cascade", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := (&ConfigWriter{Path: path}).Set("conductor.deny_task_classes", `["code"]`)
	if err != nil {
		t.Fatalf("config set conductor.deny_task_classes: %v", err)
	}
	if res.KeyPath != ConductorDenyKey {
		t.Fatalf("wrote key %q, want %q", res.KeyPath, ConductorDenyKey)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("config.toml after set:\n%s", raw)
	if !strings.Contains(string(raw), "deny_task_classes") || !strings.Contains(string(raw), `"code"`) {
		t.Fatalf("config.toml does not carry the deny list:\n%s", raw)
	}
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" },
		Environ: func() []string { return nil }, Warn: func(string, ...interface{}) {}})
	if err != nil {
		t.Fatalf("Load after set: %v", err)
	}
	got, err := ConductorDenyTaskClasses(cfg)
	if err != nil || !reflect.DeepEqual(got, []string{"code"}) {
		t.Fatalf("ConductorDenyTaskClasses = %v, %v; want [code]", got, err)
	}
}

// TestConductorDenyTaskClassesShapes is the decoder's whole table.
func TestConductorDenyTaskClassesShapes(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]interface{}
		want  []string
		kind  cascade.Kind
		msg   string
	}{
		{name: "no conductor table", extra: nil, want: []string{}},
		{name: "no key", extra: map[string]interface{}{"conductor": map[string]interface{}{}}, want: []string{}},
		{name: "listed", extra: deny([]interface{}{"code", "chat"}), want: []string{"code", "chat"}},
		{name: "string", extra: deny("code"), kind: cascade.KindInvalidInput, msg: "must be an array"},
		{name: "non-string element", extra: deny([]interface{}{"code", int64(3)}), kind: cascade.KindInvalidInput, msg: "[1] must be a non-empty"},
		{name: "empty element", extra: deny([]interface{}{""}), kind: cascade.KindInvalidInput, msg: "[0] must be a non-empty"},
		{name: "conductor not a table", extra: map[string]interface{}{"conductor": "x"}, kind: cascade.KindInvalidInput, msg: "must be a table"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ConductorDenyTaskClasses(&Config{Extra: c.extra})
			if c.msg == "" {
				if err != nil || !reflect.DeepEqual(got, c.want) {
					t.Fatalf("got %v, %v; want %v", got, err, c.want)
				}
				return
			}
			if !cascade.HasKind(err, c.kind) || !strings.Contains(err.Error(), ConductorDenyKey) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("err = %v; want kind %v naming %s and %q", err, c.kind, ConductorDenyKey, c.msg)
			}
		})
	}
	if _, err := ConductorDenyTaskClasses(nil); !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), ConductorDenyKey) {
		t.Fatalf("nil config: err = %v, want KindUnavailable naming the key", err)
	}
}

func deny(v interface{}) map[string]interface{} {
	return map[string]interface{}{"conductor": map[string]interface{}{"deny_task_classes": v}}
}
