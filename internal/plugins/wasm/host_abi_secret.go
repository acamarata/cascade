package wasm

import (
	"context"

	"github.com/tetratelabs/wazero/api"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: host_secretref (task 6, first half): resolves a plugin's
//
//	named secret request to a scoped, short-lived vault REFERENCE token,
//	never the literal value (§5.21 — credential literals are forbidden
//	in host-fn payloads). This file cannot depend on
//	internal/secrets.Broker directly (plugins-providers-boundary), so
//	SecretBroker is this package's own local seam; a composition root
//	adapts the real broker's "create a scoped reference for name" call
//	into it. Structurally, SecretRefResponse (doc.go) has no field that
//	could carry a literal even if a broken SecretBroker implementation
//	tried to return one through this path — CreateRef's own return type
//	is a bare string, and hostSecretRefFn never reads any other field off
//	whatever the broker returns.

// SecretBroker is host_secretref's delegate: the vault credential
// broker's reference-issuing surface. CreateRef must never return the
// secret's literal value — only an opaque reference token the broker
// resolves under a short-lived scoped token at call time.
type SecretBroker interface {
	CreateRef(ctx context.Context, name string) (refID string, err error)
}

// hostSecretRefFn builds the host_secretref wazero function bound to cs.
func hostSecretRefFn(cs *callState) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		var req SecretRefRequest
		if err := readRequest(m, ptr, length, &req); err != nil {
			return writeErr(m, err)
		}
		if req.Name == "" {
			return writeErr(m, cascade.New(cascade.KindInvalidInput, "wasm: host_secretref: name is empty"))
		}
		if cs.deps.Secrets == nil {
			return writeErr(m, missingDep(hostFnSecretRef))
		}
		refID, err := cs.deps.Secrets.CreateRef(ctx, req.Name)
		if err != nil {
			return writeErr(m, err)
		}
		return writeOK(m, SecretRefResponse{RefID: refID})
	}
}
