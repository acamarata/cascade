package graph

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrameIncludesNonceAndBase64Files(t *testing.T) {
	frame, err := Frame("n1", map[string][]byte{"a.txt": []byte("hi")})
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}

	s := string(frame)
	if !strings.Contains(s, "n1") {
		t.Fatalf("frame does not contain nonce n1: %s", s)
	}
	if !strings.Contains(s, "aGk=") {
		t.Fatalf("frame does not contain base64 file content aGk=: %s", s)
	}
}

func TestParseFrameRoundTrip(t *testing.T) {
	files := map[string][]byte{
		"a.txt":   []byte("hi"),
		"b.bin":   {0x00, 0x01, 0xff},
		"dir/cgo": []byte("package main\n"),
	}

	frame, err := Frame("nonce-2", files)
	if err != nil {
		t.Fatalf("Frame: %v", err)
	}

	nonce, got, err := ParseFrame(frame)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if nonce != "nonce-2" {
		t.Fatalf("nonce = %q, want %q", nonce, "nonce-2")
	}
	if len(got) != len(files) {
		t.Fatalf("got %d files, want %d", len(got), len(files))
	}
	for name, want := range files {
		data, ok := got[name]
		if !ok {
			t.Fatalf("missing file %s", name)
		}
		if !bytes.Equal(data, want) {
			t.Fatalf("file %s = %q, want %q", name, data, want)
		}
	}
}

func TestFrameEmptyNonceFails(t *testing.T) {
	if _, err := Frame("", map[string][]byte{"a.txt": []byte("hi")}); err == nil {
		t.Fatal("Frame with empty nonce succeeded, want error")
	}
}

func TestNodeIdentityFromLane(t *testing.T) {
	cases := []struct {
		lane string
		want string
	}{
		{lane: "LM@node-a", want: "node-a"},
		{lane: "CMD@node-b", want: "node-b"},
	}

	for _, tc := range cases {
		got, err := NodeIdentity(tc.lane)
		if err != nil {
			t.Fatalf("NodeIdentity(%q): %v", tc.lane, err)
		}
		if got != tc.want {
			t.Fatalf("NodeIdentity(%q) = %q, want %q", tc.lane, got, tc.want)
		}
	}
}

func TestNodeIdentityBadLaneFails(t *testing.T) {
	if _, err := NodeIdentity("node-a"); err == nil {
		t.Fatal("NodeIdentity(node-a) succeeded, want error")
	}
}
