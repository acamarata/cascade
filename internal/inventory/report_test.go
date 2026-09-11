package inventory

import (
	"strings"
	"testing"
)

func TestReport_PlatformCount(t *testing.T) {
	r := Report{Platforms: []string{"darwin", "linux", "windows"}}
	if got := r.PlatformCount(); got != 3 {
		t.Fatalf("PlatformCount() = %d, want 3", got)
	}
}

func TestReport_String(t *testing.T) {
	r := Report{
		GeneratedAt:    "2026-01-01T00:00:00Z",
		ErrorKinds:     14,
		StorageDomains: 11,
		CLICommands:    42,
		Providers:      11,
		Plugins:        5,
		SPORTLines:     1178,
		Platforms:      []string{"darwin", "linux", "windows"},
	}
	s := r.String()
	for _, want := range []string{"14", "11", "42", "5", "1178", "darwin"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}
