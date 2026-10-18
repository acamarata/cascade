//go:build !(darwin && cgo)

// Purpose: the backend of every build that cannot reach the Keychain
//
//	(CGO_ENABLED=0 or not darwin): status says unavailable, every key op is
//	refused with kind unavailable.
//
// SPORT: cmd/cascade-elevate-helper backend/ADDED (P1-SHIP-15).
package main

import (
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/pkg/cascade"
)

type unavailableBackend struct{}

func newBackend() backend { return unavailableBackend{} }

func errUnavailable() error {
	return cascade.New(cascade.KindUnavailable, "the Keychain is not reachable from this build")
}

func (unavailableBackend) Available() bool                     { return false }
func (unavailableBackend) Tier() string                        { return string(elevation.TierUnavailable) }
func (unavailableBackend) CGO() bool                           { return false }
func (unavailableBackend) Pubkey() (string, error)             { return "", errUnavailable() }
func (unavailableBackend) EnrollmentAbsent() error             { return errUnavailable() }
func (unavailableBackend) Generate() error                     { return errUnavailable() }
func (unavailableBackend) Sign([]byte, string) ([]byte, error) { return nil, errUnavailable() }
func (unavailableBackend) Presence(string) error               { return errUnavailable() }
func (unavailableBackend) DeleteKey() error                    { return errUnavailable() }
