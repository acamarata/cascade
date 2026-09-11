//go:build windows

package process

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestLaunchRefusesOnWindows(t *testing.T) {
	rt := NewProcessRuntime()
	rt.Stderr = &bytes.Buffer{}
	h, err := rt.Launch(context.Background(), Manifest{Name: "demo", TrustTier: TrustTierTrusted})
	if h != nil {
		t.Fatalf("expected a nil handle on windows, got %v", h)
	}
	if !errors.Is(err, ErrPlatformNotSupported) {
		t.Fatalf("expected ErrPlatformNotSupported, got %v", err)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("expected KindUnsupported, got %v", err)
	}
}

func TestNewProcessRuntimeWindowsZeroCost(t *testing.T) {
	if rt := NewProcessRuntime(); rt == nil {
		t.Fatal("expected a non-nil ProcessRuntime")
	}
}
