package plugin_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"github.com/acamarata/cascade/pkg/plugin"
	"testing"
)

func TestDefaultRegistryPubkeyIsEd25519Size(t *testing.T) {
	a := plugin.DefaultRegistryPublicKey()
	if len(a) != ed25519.PublicKeySize {
		t.Fatal("default is not an Ed25519 key")
	}
	a[0] ^= 1
	if bytes.Equal(a, plugin.DefaultRegistryPublicKey()) {
		t.Fatal("default aliases backing storage")
	}
}

func TestDefaultRegistryPubkeyIsNotTheFixtureKey(t *testing.T) {
	fixture, err := base64.StdEncoding.DecodeString(testRegistryPublicKeyB64)
	if err != nil || len(fixture) != 32 {
		t.Fatalf("invalid fixture: %v", err)
	}
	if bytes.Equal(plugin.DefaultRegistryPublicKey(), fixture) {
		t.Fatal("fixture promoted to production")
	}
}
