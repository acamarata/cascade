// Package topologycheck holds the four read-only fleet-topology doctor
// checks, all in group providers: free_domain_above_public,
// discover_domain_above_public, credential_without_domain and
// domain_without_credential.
//
// It is a leaf: only the cmd/cascade composition root imports it, so
// package internal/doctor (which internal/repo reaches through
// internal/secrets) never depends on internal/fleet. The package registers
// nothing itself; the composition root mounts each constructor.
package topologycheck
