// Purpose: coverage for node_admit.go's own decode/parse helpers and
//
//	runNodeAdmit's error branches, which node_test.go's enroll table only
//	exercises up to the host-key-verify step (it never reaches payload
//	decoding, compose failures, or admitNode's own EnrollNode call).
//
// SPORT: cmd/cascade/node enroll (ADD tests, P1-E17-W4-S36-T4 coverage follow-up).
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDecodeNodeAdmitPayload_Empty(t *testing.T) {
	if _, err := decodeNodeAdmitPayload(nil); err == nil {
		t.Fatal("expected refusal for an empty payload")
	}
}

func TestDecodeNodeAdmitPayload_InvalidJSON(t *testing.T) {
	if _, err := decodeNodeAdmitPayload([]byte("not json")); err == nil {
		t.Fatal("expected refusal for invalid JSON")
	}
}

func TestDecodeNodeAdmitPayload_MissingFields(t *testing.T) {
	if _, err := decodeNodeAdmitPayload([]byte(`{"node_id":"n1"}`)); err == nil {
		t.Fatal("expected refusal for a payload missing required fields")
	}
}

func TestDecodeNodeAdmitPayload_Valid(t *testing.T) {
	p, err := decodeNodeAdmitPayload([]byte(`{"node_id":"n1","node_pubkey_b64":"cGs=","node_signature_b64":"c2ln"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.NodeID != "n1" {
		t.Fatalf("got NodeID %q, want n1", p.NodeID)
	}
}

func TestSplitUserHost_InvalidFormat(t *testing.T) {
	cases := []string{"no-at-sign", "@nohost", "user@"}
	for _, c := range cases {
		if _, _, err := splitUserHost(c); err == nil {
			t.Errorf("case %q: expected refusal", c)
		}
	}
}

func TestNodeAdmitCmd_Execute_MissingTrustTier(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	cmd := newNodeAdmitCmd(deps)
	cmd.SetArgs([]string{"worker@host1"})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("got %v (ok=%v), want KindInvalidInput", err, ok)
	}
}

func TestRunNodeAdmit_RefusedOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = "windows"
	err := runNodeAdmit(fakeCmdWithContext(t), deps, "worker@host1", "worker-trusted", "", "")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("got %v (ok=%v), want KindUnsupported", err, ok)
	}
}

func TestRunNodeAdmit_ElevationRefused(t *testing.T) {
	deps := testNodeCLIDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	err := runNodeAdmit(fakeCmdWithContext(t), deps, "worker@host1", "worker-trusted", "", "")
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
		t.Fatalf("got %v (ok=%v), want ELEVATION_REQUIRED", err, ok)
	}
}

func TestRunNodeAdmit_SplitUserHostErrorPropagates(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	err := runNodeAdmit(fakeCmdWithContext(t), deps, "not-a-valid-host-arg", "worker-trusted", "", "")
	if err == nil {
		t.Fatal("expected a refusal for a malformed user@host argument")
	}
}

func TestRunNodeAdmit_ComposeErrorPropagates(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	deps.Paths = emptyDataDirPaths{}
	err := runNodeAdmit(fakeCmdWithContext(t), deps, "worker@host1", "worker-trusted", "", "")
	if err == nil {
		t.Fatal("expected a refusal for an unresolvable data directory")
	}
}

// TestRunNodeAdmit_PayloadLoadErrorPropagates drives PAST the host-key
// dial (a fake Dialer whose fingerprint matches the operator override,
// so KnownHosts.Verify succeeds) to reach loadNodeAdmitPayload's own
// error branch: empty stdin, no --payload-file.
func TestRunNodeAdmit_PayloadLoadErrorPropagates(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	deps.Dialer = fakeVerifyOnlyDialer{fingerprint: "matching-fp"}
	err := runNodeAdmit(fakeCmdWithContext(t), deps, "worker@host1", "worker-trusted", "matching-fp", "")
	if err == nil {
		t.Fatal("expected a refusal for an empty node payload")
	}
}

// TestRunNodeAdmit_AdmitNodeErrorPropagates drives past both the dial
// and the payload decode to admitNode's own EnrollNode call, which
// refuses a payload whose pubkey does not decode to a real identity.
func TestRunNodeAdmit_AdmitNodeErrorPropagates(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	deps.Dialer = fakeVerifyOnlyDialer{fingerprint: "matching-fp"}
	cmd := fakeCmdWithContext(t)
	cmd.SetIn(strings.NewReader(`{"node_id":"x","node_pubkey_b64":"not-a-real-key","node_signature_b64":"c2ln"}`))
	err := runNodeAdmit(cmd, deps, "worker@host1", "worker-trusted", "matching-fp", "")
	if err == nil {
		t.Fatal("expected EnrollNode to refuse a payload with an unparseable public key")
	}
}
