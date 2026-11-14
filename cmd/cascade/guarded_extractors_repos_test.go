package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNodesFamilyModelsRepos(t *testing.T) {
	cfg, _ := loadBody(t, "[nodes.repos]\nnode-a = [\"r2\",\"r1\"]\n")
	got, err := extractNodesFamily(cfg.Extra)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"repos":{"node-a":["r1","r2"]}}` {
		t.Fatalf("extract = %s", got)
	}
	base := "[nodes.repos]\nnode-a = [\"r1\"]\n"
	for _, after := range []string{
		"[nodes.repos]\nnode-a = [\"r1\",\"r2\"]\n",
		"[nodes.repos]\nnode-a = [\"r1\"]\nnode-b = [\"r1\"]\n",
	} {
		out := reload(t, base, after)
		if !out.ElevationRequired || !slices.ContainsFunc(pathKeys(out.LooseningPaths), func(k string) bool { return strings.HasPrefix(k, "nodes.repos") }) {
			t.Fatalf("reload to %q: elevation=%v paths=%v", after, out.ElevationRequired, pathKeys(out.LooseningPaths))
		}
		if back := reload(t, after, base); !back.Accepted {
			t.Fatalf("tightening to %q refused: %+v", base, back)
		}
	}
	bad, _ := loadBody(t, "[nodes.repos]\nnode-a = \"r1\"\n")
	_, err = extractNodesFamily(bad.Extra)
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("malformed err %v, want KindInvalidInput", err)
	}
}
