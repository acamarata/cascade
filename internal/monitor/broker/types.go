package broker

import (
	"errors"
	"time"
)

const HostDevice = "host"

const LaneStaleAfter = 5 * time.Minute

var ErrNoDevice = errors.New("broker: no device")

var ErrNoFreshLedger = errors.New("broker: no fresh ledger")

type Beat struct {
	NodeID    string    `json:"node_id"`
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
}

type Device struct {
	NodeID   string `json:"node_id"`
	LastBeat Beat   `json:"last_beat"`
	Stale    bool   `json:"stale"`
}

type AdmitLimits struct {
	MaxInflight     int
	CompileClassCap int
}

type Registry interface{}

type Sampler interface{}
