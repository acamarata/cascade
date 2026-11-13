// Package helm implements the helm control loop.
package helm

import (
	"context"
	"errors"
	"time"
)

// ErrNotImplemented indicates that the requested helm operation is unavailable.
var ErrNotImplemented = errors.New("helm: operation unavailable")

// Share contains shared resources for the helm loop.
type Share struct{}

// DB contains database resources for the helm loop.
type DB struct{}

// Client is the interface for the game controller client.
type Client interface{}

// Run executes the helm control loop. It currently returns ErrNotImplemented.
func Run(ctx context.Context, share Share, db *DB, gc Client, tick time.Duration) error {
	return ErrNotImplemented
}
