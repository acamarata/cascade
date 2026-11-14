// Package broker admits work to devices from their heartbeat ledger.
package broker

import (
	"errors"
	"time"
)

// HostDevice is the device name of the controller host itself.
const HostDevice = "host"

// ErrNoDevice is returned when no device can take the work.
var ErrNoDevice = errors.New("broker: no device")

// Beat is one heartbeat from a node.
type Beat struct {
	NodeID    string    `json:"node_id"`
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
}

// Device is a node as the broker sees it: its last beat and whether it is stale.
type Device struct {
	NodeID   string `json:"node_id"`
	LastBeat Beat   `json:"last_beat"`
	Stale    bool   `json:"stale"`
}

// AdmitLimits caps the work the broker admits at once.
type AdmitLimits struct {
	MaxInflight     int
	CompileClassCap int
}

// Registry lists the devices the broker may admit work to.
type Registry interface{}

// Sampler reads the current load of a device.
type Sampler interface{}
