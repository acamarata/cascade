package graph

import "testing"

// TestReadyAllConditionsMet verifies a device is ready when all conditions are satisfied.
func TestReadyAllConditionsMet(t *testing.T) {
	d := Device{
		Healthy:       true,
		Draining:      false,
		VolumeMounted: true,
		BatteryLevel:  20,
		PluggedIn:     false,
	}
	if !DeviceReady(d) {
		t.Error("expected device to be ready")
	}
}

// TestUnreadyWhenUnhealthy verifies a device is unready when unhealthy.
func TestUnreadyWhenUnhealthy(t *testing.T) {
	d := Device{
		Healthy:       false,
		Draining:      false,
		VolumeMounted: true,
		BatteryLevel:  100,
		PluggedIn:     true,
	}
	if DeviceReady(d) {
		t.Error("expected device to be unready when unhealthy")
	}
}

// TestUnreadyWhenDraining verifies a device is unready when draining.
func TestUnreadyWhenDraining(t *testing.T) {
	d := Device{
		Healthy:       true,
		Draining:      true,
		VolumeMounted: true,
		BatteryLevel:  100,
		PluggedIn:     true,
	}
	if DeviceReady(d) {
		t.Error("expected device to be unready when draining")
	}
}

// TestUnreadyWhenUnmounted verifies a device is unready when volume is not mounted.
func TestUnreadyWhenUnmounted(t *testing.T) {
	d := Device{
		Healthy:       true,
		Draining:      false,
		VolumeMounted: false,
		BatteryLevel:  100,
		PluggedIn:     true,
	}
	if DeviceReady(d) {
		t.Error("expected device to be unready when unmounted")
	}
}

// TestUnreadyLowBatteryUnplugged verifies a device is unready when unplugged with low battery.
func TestUnreadyLowBatteryUnplugged(t *testing.T) {
	d := Device{
		Healthy:       true,
		Draining:      false,
		VolumeMounted: true,
		BatteryLevel:  19,
		PluggedIn:     false,
	}
	if DeviceReady(d) {
		t.Error("expected device to be unready with 19% battery unplugged")
	}
}

// TestReadyLowBatteryPluggedIn verifies a device is ready when plugged in with low battery.
func TestReadyLowBatteryPluggedIn(t *testing.T) {
	d := Device{
		Healthy:       true,
		Draining:      false,
		VolumeMounted: true,
		BatteryLevel:  5,
		PluggedIn:     true,
	}
	if !DeviceReady(d) {
		t.Error("expected device to be ready when plugged in with low battery")
	}
}
