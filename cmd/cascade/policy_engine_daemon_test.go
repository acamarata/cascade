//go:build !windows

// Purpose: the daemon half of the unfollowable-config guard. The daemon loads
//
//	its config with runtime.Load, which reads a config.toml it cannot follow
//	as "no config file"; wirePolicy must refuse that rather than run the
//	default profile, as the MCP and github-ci surfaces do.
//
// SPORT: cmd/cascade/policy (ADD) -- constructor verification.
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

func TestWirePolicyRefusesAnUnfollowableConfig(t *testing.T) {
	ctx := context.Background()
	paths := auditTestPaths(t)
	clock := testkit.NewFrozenClock(policyTestEpoch)
	if _, err := wirePolicy(ctx, storetest.NewMemStore(), clock, nil, paths); err != nil {
		t.Fatalf("control: wirePolicy with no config.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath()), 0o700); err != nil {
		t.Fatalf("config dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone.toml"), paths.ConfigPath()); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := wirePolicy(ctx, storetest.NewMemStore(), clock, nil, paths); err == nil {
		t.Fatal("wirePolicy ran the default profile over a config.toml it cannot follow")
	}
}
