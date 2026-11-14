package crunch

import (
	"errors"
	"fmt"
)

// ErrD0583 is the error returned when an operation attempts to route through the Broker queue or is invalid.
var ErrD0583 = errors.New("D0583")

// Route evaluates the operation type and returns the correct scheduler target.
// It ensures crunch operations remain on gatewaysat/gatewaysched without passing through the Broker queue.
func Route(op string) (string, error) {
	switch op {
	case "crunch-op-a":
		return "gatewaysat", nil
	case "crunch-op-b":
		return "gatewaysched", nil
	case "broker-route":
		return "", fmt.Errorf("%w: %s", ErrD0583, op)
	default:
		return "", fmt.Errorf("%w: %s", ErrD0583, op)
	}
}
