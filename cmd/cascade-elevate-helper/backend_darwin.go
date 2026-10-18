//go:build darwin && cgo

// Purpose: the real backend: the in-process darwin keystore plus the two
//
//	helper-only operations (sign with a helper-built prompt, delete behind a
//	presence check).
//
// SPORT: cmd/cascade-elevate-helper backend/ADDED (P1-SHIP-15).
package main

import "github.com/acamarata/cascade/internal/elevation"

type darwinBackend struct{ ks elevation.ElevationKeystore }

func newBackend() backend { return darwinBackend{ks: elevation.NewKeystore()} }

// Available is false without the storage entitlement: the Keychain refuses to
// store an access-controlled item without one (see docs/elevation-helper.md),
// so claiming availability would only fail later at enrollment.
func (b darwinBackend) Available() bool { return hasStorageEntitlement() && b.ks.IsAvailable() }

func (b darwinBackend) Tier() string {
	if !b.Available() {
		return string(elevation.TierUnavailable)
	}
	return string(b.ks.Tier())
}
func (darwinBackend) CGO() bool                 { return true }
func (b darwinBackend) Pubkey() (string, error) { return b.ks.PubKeyB64() }
func (darwinBackend) Generate() error           { return elevation.GenerateKeyNoReplace() }
func (darwinBackend) EnrollmentAbsent() error   { return elevation.EnrollmentAbsent() }
func (darwinBackend) Sign(payload []byte, reason string) ([]byte, error) {
	return elevation.SignWithReason(payload, reason)
}
func (darwinBackend) Presence(reason string) error { return elevation.DevicePresence(reason) }
func (darwinBackend) DeleteKey() error             { return elevation.DeletePlatformKeyItems() }
