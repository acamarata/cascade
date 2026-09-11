//go:build !postgres

// Purpose: the !postgres twin of profile_server.go. A binary built without
//
//	-tags=postgres never links providers/postgres, providers/pgvector,
//	providers/redis or providers/s3 (profile_server.go, which alone
//	imports all four, is itself //go:build postgres), so `--profile
//	server` refuses with a named, typed error instead of silently running
//	against local storage — Art.1's anti-stub rule applies to this
//	refusal too: it must say exactly why, never pretend to have tried.
//
// SPORT: cmd/cascade.profile-server/ADDED (P1-E17-W4-S38-T4).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// serverProfileBuildSupported is false in every binary built without
// -tags=postgres — see profile_server.go's twin constant.
const serverProfileBuildSupported = false

// assembleServerProfile always refuses: this binary was not built with
// -tags=postgres, so providers/postgres and providers/pgvector are not
// linked in at all.
func assembleServerProfile(_ context.Context, _ runtime.EnvLookup, _ runtime.Clock) (*runtime.ServerProfile, error) {
	return nil, cascade.New(cascade.KindUnsupported,
		"cascade: --profile server requires a binary built with -tags=postgres (providers/postgres and providers/pgvector are not linked into this binary)")
}
