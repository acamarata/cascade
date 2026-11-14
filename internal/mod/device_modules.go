// Package mod provides module validation utilities.
package mod

import (
	"errors"
	"fmt"
	"strings"
)

// ErrModuleMoved is returned when a device automation module is located outside the cam package.
var ErrModuleMoved = errors.New("module moved")

// CheckDeviceModules checks that device automation modules (nas, mac, iot) are located inside the cam package.
// It returns ErrModuleMoved mentioning the module name if any maps to a location outside cam.
func CheckDeviceModules(modules map[string]string) error {
	deviceModules := map[string]bool{
		"nas": true,
		"mac": true,
		"iot": true,
	}

	for name, loc := range modules {
		if !deviceModules[name] {
			continue
		}
		if !strings.HasPrefix(loc, "cam/") && loc != "cam" {
			return fmt.Errorf("%w: %s", ErrModuleMoved, name)
		}
	}
	return nil
}
