package plugins

// Purpose: prove installer firewall construction and refusal before fetching.
// Inputs: isolated file custody and factory failures. Outputs: no cached bytes.
// Constraints: no host custody or sockets. SPORT: registry-fetch enforcement.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/bridge"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func TestRegistryArtifactFirewallWiring(t *testing.T) {
	for _, mode := range []string{"factory-error", "capability-error", "configured"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
				t.Setenv(name, dir)
			}
			original := registryArtifactFirewall
			t.Cleanup(func() { registryArtifactFirewall = original })
			failure := cascade.New(cascade.KindUnavailable, "isolated firewall refused")
			called := false
			registryArtifactFirewall = func(cfg secrets.Config) (*egress.Engine, error) {
				called = true
				if cfg.Service != secrets.DefaultVaultService || cfg.Dir != dir {
					t.Fatalf("firewall custody config = %+v", cfg)
				}
				if mode == "factory-error" {
					return nil, failure
				}
				if mode == "capability-error" {
					return &egress.Engine{}, nil
				}
				cfg.ForceFileVault = true
				return bridge.NewFirewall(cfg)
			}
			a := &installerAdapter{
				resolvePaths:   func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil },
				registryURL:    func() (string, error) { return "https://registry.example/", nil },
				registryPubKey: func() (ed25519.PublicKey, error) { return make(ed25519.PublicKey, ed25519.PublicKeySize), nil },
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Even a broken refusal cannot reach a socket.
			entry := plugin.RegistryVersionEntry{Version: "1", Checksum: "present", Signature: "present", DownloadURL: "https://registry.example/artifact"}
			data, err := a.verifiedArtifactBytes(ctx, "example", entry)
			if !called || data != nil || len(a.verified) != 0 {
				t.Fatalf("factory=%v bytes=%q cache=%v error=%v", called, data, a.verified, err)
			}
			assertRegistryFirewallError(t, mode, err, failure)
		})
	}
}

func assertRegistryFirewallError(t *testing.T, mode string, err, failure error) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("error=%v, want unavailable", err)
	}
	switch mode {
	case "factory-error":
		if err != failure || err.Error() != failure.Error() {
			t.Fatalf("error=%v, want exact factory refusal %v", err, failure)
		}
	case "capability-error":
		if err.Error() != "unavailable: egress: no engine configured" {
			t.Fatalf("error=%v, want capability refusal before fetch", err)
		}
	case "configured":
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "registryfetch: GET") {
			t.Fatalf("error=%v, want configured fetch canceled before transport", err)
		}
	}
}
