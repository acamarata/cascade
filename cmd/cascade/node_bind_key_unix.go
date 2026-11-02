//go:build !windows

// Purpose: the controller installation public key `node enroll` prints into
// the binding (contract:controller-key-binding), on platforms that hold the
// installation key.
// SPORT: cmd/cascade controllerPubKeyB64/ADDED (P1-CI-13).
package main

import (
	"context"
	"encoding/base64"

	"github.com/acamarata/cascade/internal/runtime"
)

// controllerPubKeyB64 is standard base64 of installationPublicKey (R21b B-2),
// never the controller's per-node identity key.
func controllerPubKeyB64(ctx context.Context, paths runtime.PathProvider) (string, error) {
	pub, err := installationPublicKey(ctx, paths)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}
