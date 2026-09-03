package rpc

import (
	"strings"
	"testing"
)

func TestSkewCheck_NoClientVersion(t *testing.T) {
	if err := SkewCheck(""); err != nil {
		t.Fatalf("empty client_version must not be a skew: %+v", err)
	}
}

func TestSkewCheck_MatchingMajor(t *testing.T) {
	if err := SkewCheck(ProtocolVersion); err != nil {
		t.Fatalf("matching client_version must not be a skew: %+v", err)
	}
}

func TestSkewCheck_MajorMismatch(t *testing.T) {
	err := SkewCheck("999.0.0")
	if err == nil {
		t.Fatal("expected a skew error")
	}
	if err.Code != codeInvalidRequest {
		t.Errorf("Code = %d, want %d", err.Code, codeInvalidRequest)
	}
	if !containsAll(err.Message, "999", ProtocolVersion) {
		t.Errorf("message %q must name both versions", err.Message)
	}
}

func TestSkewCheck_UnparseableVersion(t *testing.T) {
	err := SkewCheck("not-a-version")
	if err == nil || err.Code != codeInvalidRequest {
		t.Fatalf("expected codeInvalidRequest for unparseable version, got %+v", err)
	}
}

func TestParseClientVersion(t *testing.T) {
	cases := []struct {
		in        string
		wantMajor int
		wantOK    bool
	}{
		{"1.2.3", 1, true},
		{"2.0.0-rc1", 2, true},
		{"5", 5, true},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		major, ok := ParseClientVersion(c.in)
		if major != c.wantMajor || ok != c.wantOK {
			t.Errorf("ParseClientVersion(%q) = (%d, %v), want (%d, %v)", c.in, major, ok, c.wantMajor, c.wantOK)
		}
	}
}

func TestNewEnvelope(t *testing.T) {
	env := NewEnvelope(nil, map[string]int{"x": 1}, nil)
	if env.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", env.ProtocolVersion, ProtocolVersion)
	}
	if env.ServerVersion == "" {
		t.Error("ServerVersion must not be empty")
	}
	if env.JSONRPC != jsonrpcVersion {
		t.Errorf("JSONRPC = %q, want %q", env.JSONRPC, jsonrpcVersion)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
