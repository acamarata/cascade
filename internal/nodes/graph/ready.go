// Package graph provides node graph utilities.
package graph

// Device represents a node device with health, drain, mount and battery state.
type Device struct {
	Healthy       bool
	Draining      bool
	VolumeMounted bool
	BatteryLevel  int
	PluggedIn     bool
}

// DeviceReady returns true only when the device is healthy, not draining,
// has its volume mounted, and (if unplugged) has battery >= 20%.
func DeviceReady(d Device) bool {
	if !d.Healthy {
		return false
	}
	if d.Draining {
		return false
	}
	if !d.VolumeMounted {
		return false
	}
	if !d.PluggedIn && d.BatteryLevel < 20 {
		return false
	}
	return true
}
