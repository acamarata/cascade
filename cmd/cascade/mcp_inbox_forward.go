// Purpose: forward inbox reads with the session principal and strict inputs.
// Constraints: never send an owner marker or widen the session's inbox scope.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// registerMCPInboxMethods binds the session-only inbox reads.
func registerMCPInboxMethods(reg *rpc.Registry, paths runtime.PathProvider) {
	token := os.Getenv(principal.EnvToken)
	for _, method := range []string{"inbox.list", "inbox.read"} {
		if token == "" {
			reg.Register(method, refuseWith(errMCPInboxPrincipal))
			continue
		}
		c, err := dialMCPDaemon(paths)(token)
		if err != nil {
			reg.Register(method, refuseUnreachable(method, err))
			continue
		}
		reg.Register(method, mcpInboxForwarder(method, scopedForwarder(c, method)))
	}
}

var errMCPInboxPrincipal = cascade.New(cascade.KindPermissionDenied,
	"mcp: inbox reads require a caller principal token; refused")

// mcpInboxForwarder preserves inbox schemas when principal mode rebinds methods.
func mcpInboxForwarder(method string, next mcpHandler) mcpHandler {
	if method != "inbox.list" && method != "inbox.read" {
		return next
	}
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		var input map[string]any
		decoder := json.NewDecoder(bytes.NewReader(params))
		if err := decoder.Decode(&input); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "mcp: invalid inbox arguments")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, cascade.New(cascade.KindInvalidInput, "mcp: inbox arguments must be one object")
		}
		if err := validateMCPInboxInput(method, input); err != nil {
			return nil, err
		}
		if method == "inbox.read" {
			if _, ok := input["id"].(string); !ok {
				return nil, missingInboxID(ctx, next)
			}
		}
		return next(ctx, params)
	}
}

// missingInboxID refuses an inbox.read with no id. The schema check runs
// after reachability: an empty object (no key that could widen the scope)
// goes to the daemon first, so a stopped daemon reports unavailable like
// every other forwarded tool, and a reachable one gets the schema error.
func missingInboxID(ctx context.Context, next mcpHandler) error {
	_, err := next(ctx, json.RawMessage(`{}`))
	for _, kind := range []cascade.Kind{cascade.KindUnavailable, cascade.KindTimeout, cascade.KindCanceled} {
		if cascade.HasKind(err, kind) {
			return err
		}
	}
	return errMCPInboxSchema()
}

func errMCPInboxSchema() error {
	return cascade.New(cascade.KindInvalidInput, "mcp: invalid inbox arguments for tool schema")
}

// validateMCPInboxInput enforces the two read-only inbox tool schemas.
func validateMCPInboxInput(method string, input map[string]any) error {
	invalid := errMCPInboxSchema()
	if input == nil {
		return invalid
	}
	for key, value := range input {
		if method == "inbox.list" && key == "unread" {
			if _, ok := value.(bool); ok {
				continue
			}
		}
		if method == "inbox.read" && key == "id" {
			if _, ok := value.(string); ok {
				continue
			}
		}
		return invalid
	}
	return nil
}
