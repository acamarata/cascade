package broker

import "errors"

// ErrInvalidLane is returned when a capacity lookup is requested for a lane
// that the broker does not recognise.
var ErrInvalidLane = errors.New("D3813: invalid lane")

// Limit describes the broker-local capacity limits for a lane.
type Limit struct {
	// RPM is the maximum number of requests per minute allowed for the lane.
	RPM int

	// InFlight is the maximum number of concurrent in-flight requests allowed for the lane.
	InFlight int
}

// CapacityLimit returns the broker-local capacity limit for lane.
// Only "lane-default" is supported; any other lane returns a zero Limit and ErrInvalidLane.
func CapacityLimit(lane string) (Limit, error) {
	if lane == "lane-default" {
		return Limit{RPM: 60, InFlight: 10}, nil
	}

	return Limit{}, ErrInvalidLane
}
