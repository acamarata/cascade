package main

import (
	"os"
	"strings"
	"testing"
)

// TestProviderExecInGoldenHelpAndDaemonDoc keeps provider exec discoverable.
func TestProviderExecInGoldenHelpAndDaemonDoc(t *testing.T) {
	for _, path := range []string{
		"testdata/golden_help.txt",
		"../../docs/daemon.md",
	} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read documentation: %v", err)
			}
			if !strings.Contains(string(data), "provider exec") {
				t.Error("documentation must mention provider exec")
			}
		})
	}
}
