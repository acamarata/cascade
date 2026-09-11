package inventory

import "testing"

func TestLoadGenerated(t *testing.T) {
	g, err := LoadGenerated()
	if err != nil {
		t.Fatalf("LoadGenerated: %v", err)
	}
	if g.GeneratedAt == "" {
		t.Error("LoadGenerated: GeneratedAt is empty")
	}
	if len(g.Platforms) == 0 {
		t.Error("LoadGenerated: Platforms is empty")
	}
}
