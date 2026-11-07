package main

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

func lanesTree(lanes map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"agents": map[string]interface{}{"lanes": lanes}}
}

func TestLanesFamilyRunsTheDecoder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	got, err := extractLanesFamily(lanesTree(map[string]interface{}{
		"codex":  map[string]interface{}{"posture": "programmatic"},
		"claude": map[string]interface{}{"posture": "interactive"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]map[string]string
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v["codex"]["posture"] != "programmatic" {
		t.Fatalf("family = %s", got)
	}
	bad := lanesTree(map[string]interface{}{"codex": map[string]interface{}{"posture": "bogus"}})
	_, want := runtime.ParseAgentLanes(bad)
	_, gotErr := extractLanesFamily(bad)
	if want == nil || gotErr == nil || gotErr.Error() != want.Error() {
		t.Fatalf("err = %v, want %v", gotErr, want)
	}
}
