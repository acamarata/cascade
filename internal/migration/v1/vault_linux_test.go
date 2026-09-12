//go:build linux

package v1

import (
	"context"
	"testing"
)

// This matrix test proves the importer stays custody-agnostic on Linux: its
// only platform boundary is the injected broker, and no platform keychain is
// contacted during import.
func TestVaultImporter_LinuxUsesInjectedBroker(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/vault.env", []byte("LINUX_OPAQUE=value\n"))
	custody := newMapCustody()
	_, err := NewVaultImporter(testBroker(t, custody)).Import(
		context.Background(), Request{SourceRoot: root})
	if err != nil || string(custody.values["LINUX_OPAQUE"]) != "value" {
		t.Fatalf("linux importer bypassed injected custody: values=%v err=%v", custody.values, err)
	}
}
