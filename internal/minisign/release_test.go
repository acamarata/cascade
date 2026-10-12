package minisign

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEmbeddedReleaseKeyMatchesDocs(t *testing.T) {
	docs, err := os.ReadFile("../../docs/keys/minisign.pub")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 || !bytes.Equal(docs, releasePublicKeyBytes) {
		t.Fatal("embedded release key differs from docs")
	}
	if _, err := ReleasePublicKey(); err != nil {
		t.Fatal(err)
	}
}

func TestReleasePublicKeyEmptyFailsClosed(t *testing.T) {
	old := releasePublicKeyBytes
	t.Cleanup(func() { releasePublicKeyBytes = old })
	for _, raw := range [][]byte{nil, []byte("broken")} {
		releasePublicKeyBytes = raw
		_, err := ReleasePublicKey()
		want := cascade.New(cascade.KindUnavailable, "minisign: embedded release public key unavailable")
		if !errors.Is(err, want) || err.Error() != want.Error() {
			t.Fatalf("got %v, want %v", err, want)
		}
	}
}
