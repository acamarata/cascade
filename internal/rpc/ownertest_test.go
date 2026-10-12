package rpc

import (
	"os"
	"testing"
)

// TestWithOwnerUIDForTestOverridesThenRestores proves the override is
// visible to the guard's owner lookup for the life of the test that set it,
// and that t.Cleanup puts the real owner back afterwards.
func TestWithOwnerUIDForTestOverridesThenRestores(t *testing.T) {
	uid := os.Getuid()
	if got := ownerUID(); got != uid {
		t.Fatalf("ownerUID() before override = %d, want the process UID %d", got, uid)
	}
	t.Run("override", func(t *testing.T) {
		WithOwnerUIDForTest(t, uid+1)
		if got := ownerUID(); got != uid+1 {
			t.Fatalf("ownerUID() under override = %d, want %d", got, uid+1)
		}
	})
	if got := ownerUID(); got != uid {
		t.Fatalf("ownerUID() after the subtest = %d, want the restored process UID %d", got, uid)
	}
}
