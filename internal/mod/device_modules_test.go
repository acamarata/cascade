package mod

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckDeviceModulesAllInCam(t *testing.T) {
	modules := map[string]string{
		"nas": "cam/nas",
		"mac": "cam/mac",
		"iot": "cam/iot",
	}
	err := CheckDeviceModules(modules)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckDeviceModulesNasMoved(t *testing.T) {
	modules := map[string]string{
		"nas": "other/nas",
	}
	err := CheckDeviceModules(modules)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrModuleMoved) {
		t.Fatalf("expected ErrModuleMoved, got %v", err)
	}
	if !strings.Contains(err.Error(), "nas") {
		t.Fatalf("expected error to mention 'nas', got %v", err)
	}
}

func TestCheckDeviceModulesIotMoved(t *testing.T) {
	modules := map[string]string{
		"iot": "pkg/iot",
	}
	err := CheckDeviceModules(modules)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrModuleMoved) {
		t.Fatalf("expected ErrModuleMoved, got %v", err)
	}
	if !strings.Contains(err.Error(), "iot") {
		t.Fatalf("expected error to mention 'iot', got %v", err)
	}
}

func TestCheckDeviceModulesNonDeviceIgnored(t *testing.T) {
	modules := map[string]string{
		"hooks": "other/hooks",
	}
	err := CheckDeviceModules(modules)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}
