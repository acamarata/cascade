package plugins

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestPermsRPCHandlerNeedsAWitnessForItsVerb: only an attested call for the
// handler's own verb changes grants; a witness for the other perms verb, no
// witness, a missing store and unknown params fields are refused.
func TestPermsRPCHandlerNeedsAWitnessForItsVerb(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo", InstalledVersion: "1.0.0", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"name":"demo","capability":"fake.capability"}`)
	if _, err := PermsRPCHandler(store, true)(ctx, args); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("unattested perms.grant = %v", err)
	}
	if _, err := attestedCall(t, PermsRevokeRPCMethod, args, PermsRPCHandler(store, true)); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("perms.grant under a perms.revoke witness = %v", err)
	}
	res, err := attestedCall(t, PermsGrantRPCMethod, args, PermsRPCHandler(store, true))
	if err != nil || len(res.(PermsResult).After) != 1 || len(res.(PermsResult).Before) != 0 {
		t.Fatalf("attested perms.grant = %+v %v", res, err)
	}
	res, err = attestedCall(t, PermsRevokeRPCMethod, args, PermsRPCHandler(store, false))
	if err != nil || len(res.(PermsResult).After) != 0 || len(res.(PermsResult).Before) != 1 {
		t.Fatalf("attested perms.revoke = %+v %v", res, err)
	}
	if _, err := attestedCall(t, PermsGrantRPCMethod, args, PermsRPCHandler(nil, true)); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("perms.grant with no store = %v", err)
	}
	// A witness for the verb over other params never changes grants.
	var witnessed context.Context
	if _, err := attestedCall(t, PermsGrantRPCMethod, args, func(ctx context.Context, _ json.RawMessage) (any, error) {
		witnessed = ctx
		return "witnessed", nil
	}); err != nil {
		t.Fatal(err)
	}
	other := json.RawMessage(`{"name":"demo","capability":"other.capability"}`)
	if _, err := PermsRPCHandler(store, true)(witnessed, other); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("perms.grant under a witness for other params = %v", err)
	}
	extra := json.RawMessage(`{"name":"demo","capability":"x","grant_all":true}`)
	if _, err := attestedCall(t, PermsGrantRPCMethod, extra, PermsRPCHandler(store, true)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("perms.grant with an unknown field = %v", err)
	}
}
