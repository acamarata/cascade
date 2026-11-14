package docket

import "testing"

func TestPhasesCascadeBox(t *testing.T) {
	feeds, err := Phases("cascade-box")
	if err != nil {
		t.Fatalf("Phases(cascade-box) returned error: %v", err)
	}

	names := make([]string, 0, len(feeds))
	for _, feed := range feeds {
		names = append(names, feed.Mod)
	}

	if len(names) != 1 || names[0] != "cascade-box" {
		t.Fatalf("Phases(cascade-box) rail feed names = %v, want [cascade-box]", names)
	}
}

func TestPhasesFeedRecord(t *testing.T) {
	feeds, err := Phases("cascade-box")
	if err != nil {
		t.Fatalf("Phases(cascade-box) returned error: %v", err)
	}

	want := PhasesFeed{Mod: "cascade-box", Source: "phases", Status: "ok"}
	if len(feeds) != 1 || feeds[0] != want {
		t.Fatalf("Phases(cascade-box) feed records = %#v, want [%#v]", feeds, want)
	}
}

func TestPhasesEmptyModFailsClosed(t *testing.T) {
	feeds, err := Phases("")
	if err == nil {
		t.Fatal("Phases(\"\") returned nil error, want non-nil error")
	}
	if feeds != nil {
		t.Fatalf("Phases(\"\") returned feeds %v, want nil", feeds)
	}
}
