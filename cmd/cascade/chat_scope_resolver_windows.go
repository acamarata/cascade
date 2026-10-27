//go:build windows

// Purpose: the Windows twin of chat_scope_resolver_unix.go. Windows is tier-2
// with no daemon and no recall.what resolver, so chat records no project for
// an owner thread there: unscoped, never guessed.
// SPORT: cmd/cascade chat scope resolver (ADD) -- P1-CPA-06.
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/runtime"
)

// chatScopeResolver returns no resolver on Windows.
func chatScopeResolver(runtime.PathProvider) func(context.Context, scope.ResolveInput) (string, error) {
	return nil
}
