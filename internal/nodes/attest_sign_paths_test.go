// Purpose: refusal-path tests for the ci.attest.sign handler: an unusable
// pin, an unconfigured node, a request whose payload cannot be formed, a
// signature that is not base64 and a binding store that cannot be read.
// SPORT: nodes tests (EXTEND) - P1-CI-13.
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type failingBinding struct{}

func (failingBinding) Load() (ControllerBinding, bool, error) {
	return ControllerBinding{}, false, errors.New("store offline")
}

func TestAttestSignRefusalPaths(t *testing.T) {
	ctx := context.Background()
	t.Run("pinned key is malformed", func(t *testing.T) {
		r := newAttestRig(t)
		r.deps.binding = memBinding{b: ControllerBinding{Endpoint: "e", EnrollmentID: "i", ControllerPubKeyB64: "%%%"}, ok: true}
		_, err := serveAttestSign(ctx, r.deps, r.signed(t, r.request, r.ctrl))
		wantFailure(t, err, cascade.KindPermissionDenied, "pinned controller key is unusable")
	})
	t.Run("node has no id", func(t *testing.T) {
		r := newAttestRig(t)
		params := r.signed(t, r.request, r.ctrl)
		r.deps.nodeID = ""
		_, err := serveAttestSign(ctx, r.deps, params)
		wantFailure(t, err, cascade.KindPermissionDenied, "not configured on this node")
	})
	t.Run("expiry has a sub-second part", func(t *testing.T) {
		r := newAttestRig(t)
		req := r.request
		req.ExpiresAt = r.now.Add(time.Hour + time.Millisecond)
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = serveAttestSign(ctx, r.deps, raw)
		wantFailure(t, err, cascade.KindInvalidInput, "whole-second")
	})
	t.Run("signature is not base64", func(t *testing.T) {
		r := newAttestRig(t)
		req := r.request
		req.ControllerSig = "!!not-base64!!"
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = serveAttestSign(ctx, r.deps, raw)
		wantFailure(t, err, cascade.KindPermissionDenied, "not base64")
	})
	t.Run("binding store cannot be read", func(t *testing.T) {
		r := newAttestRig(t)
		r.deps.binding = failingBinding{}
		_, err := serveAttestSign(ctx, r.deps, r.signed(t, r.request, r.ctrl))
		wantFailure(t, err, cascade.KindPermissionDenied, "controller binding cannot be read")
	})
}
