package principal

import "testing"

func TestKindNodeValue(t *testing.T) {
	if string(KindNode) != "node" {
		t.Errorf("KindNode string value = %q, want %q", string(KindNode), "node")
	}
	if KindNode == KindSession {
		t.Error("KindNode must differ from KindSession")
	}
	if KindNode == KindMCP {
		t.Error("KindNode must differ from KindMCP")
	}
	if KindNode == KindHook {
		t.Error("KindNode must differ from KindHook")
	}
	if KindNode == KindNodeJob {
		t.Error("KindNode must differ from KindNodeJob")
	}
	if KindNode == KindRemoteClient {
		t.Error("KindNode must differ from KindRemoteClient")
	}
	if KindNode == KindPlugin {
		t.Error("KindNode must differ from KindPlugin")
	}
}

func TestMinterExhaustive(t *testing.T) {
	// Verify that all declared kinds are explicitly handled by mintable
	// and that reserved kinds like KindNode and KindPlugin are not mintable.
	kinds := []Kind{
		KindSession,
		KindMCP,
		KindHook,
		KindNodeJob,
		KindRemoteClient,
		KindPlugin,
		KindNode,
	}
	for _, k := range kinds {
		_ = mintable(k)
	}
	if mintable(KindNode) {
		t.Error("KindNode must not be mintable")
	}
	if mintable(KindPlugin) {
		t.Error("KindPlugin must not be mintable")
	}
	for _, k := range []Kind{KindSession, KindMCP, KindHook, KindNodeJob, KindRemoteClient} {
		if !mintable(k) {
			t.Errorf("Kind %q should be mintable", k)
		}
	}
}
