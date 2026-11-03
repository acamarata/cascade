package antigravity

import (
	"os"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

func TestAntigravityManifestMatchesCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f, err := os.Open("manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := plugin.ParseManifest(f)
	if err != nil {
		t.Fatal(err)
	}
	want := manifest()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest file = %#v, code = %#v", got, want)
	}
	if got.ID != "antigravity" || got.Runtime != plugin.RuntimeBuiltin ||
		len(got.Provides.Commands) != 1 || got.Provides.Commands[0].Name != "detect" ||
		len(got.Provides.Tools)+len(got.Provides.Intents)+len(got.Provides.Domains) != 0 {
		t.Fatalf("unexpected manifest surface: %+v", got)
	}
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID == got.ID {
			if !reflect.DeepEqual(reg.Manifest, want) {
				t.Fatal("registered manifest differs from manifest()")
			}
			return
		}
	}
	t.Fatal("antigravity builtin not registered")
}
