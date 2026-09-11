package process

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWrapUntrusted(t *testing.T) {
	err := wrapUntrusted("demo", TrustTierUntrusted)
	if !errors.Is(err, ErrUntrustedPlugin) {
		t.Fatalf("errors.Is(err, ErrUntrustedPlugin) = false")
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("expected KindPolicyDenied, got %v", err)
	}
}

func TestWrapVersionMismatch(t *testing.T) {
	err := wrapVersionMismatch("demo", "0.9.0", "1.0.0")
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("errors.Is(err, ErrVersionMismatch) = false")
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("expected KindUnsupported, got %v", err)
	}
}

func TestWrapUnavailable(t *testing.T) {
	err := wrapUnavailable("demo", 4)
	if !errors.Is(err, ErrPluginUnavailable) {
		t.Fatalf("errors.Is(err, ErrPluginUnavailable) = false")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("expected KindUnavailable, got %v", err)
	}
}

func TestWrapPlatformNotSupported(t *testing.T) {
	err := wrapPlatformNotSupported("windows")
	if !errors.Is(err, ErrPlatformNotSupported) {
		t.Fatalf("errors.Is(err, ErrPlatformNotSupported) = false")
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("expected KindUnsupported, got %v", err)
	}
}

func TestManifestMinProtocolVersion(t *testing.T) {
	if got := (Manifest{}).minProtocolVersion(); got != PluginProtocolVersion {
		t.Fatalf("default minProtocolVersion = %q, want %q", got, PluginProtocolVersion)
	}
	if got := (Manifest{MinProtocolVersion: "2.0.0"}).minProtocolVersion(); got != "2.0.0" {
		t.Fatalf("override minProtocolVersion = %q, want 2.0.0", got)
	}
}
