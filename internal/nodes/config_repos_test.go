package nodes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func loadReposBody(t *testing.T, body string) (Section, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := runtime.DecodeConfigFile(path)
	if err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	raw, _ := doc["nodes"].(map[string]interface{})
	return LoadSection(raw)
}

func TestConfigNodesReposRejectsMalformed(t *testing.T) {
	for _, tc := range []struct{ body, key string }{
		{"[nodes]\ntravel = true\nrepos = \"x\"\n", "[nodes.repos]"},
		{"[nodes]\ntravel = true\n[nodes.repos]\nnode-a = \"r1\"\n", "node-a"},
		{"[nodes]\ntravel = true\n[nodes.repos]\nnode-a = [1]\n", "node-a"},
		{"[nodes]\ntravel = true\n[nodes.repos]\nnode-a = [\"\"]\n", "node-a"},
		{"[nodes]\ntravel = true\n[nodes.repos]\n\"\" = [\"r1\"]\n", "empty node id"},
	} {
		sec, err := loadReposBody(t, tc.body)
		if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
			t.Fatalf("%q: err %v, want KindInvalidInput", tc.body, err)
		}
		if !strings.Contains(err.Error(), tc.key) {
			t.Fatalf("%q: err %q does not name %q", tc.body, err, tc.key)
		}
		if !reflect.DeepEqual(sec, Section{}) {
			t.Fatalf("%q: partial Section %+v", tc.body, sec)
		}
	}
}

func TestConfigNodesReposParses(t *testing.T) {
	sec, err := loadReposBody(t, "[nodes.repos]\nnode-a = [\"r2\", \"r1\", \"r1\"]\nnode-b = []\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"node-a": {"r1", "r2"}, "node-b": {}}
	if !reflect.DeepEqual(sec.Repos, want) {
		t.Fatalf("Repos %#v, want %#v", sec.Repos, want)
	}
	none, err := loadReposBody(t, "[nodes]\ntravel = true\n")
	if err != nil || len(none.Repos) != 0 {
		t.Fatalf("absent: %v %+v", err, none.Repos)
	}
}
