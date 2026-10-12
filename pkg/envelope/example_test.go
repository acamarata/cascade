package envelope_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/acamarata/cascade/pkg/envelope"
)

// exampleSigner is an in-memory signer for the example; production code
// uses the controller installation key custody instead.
type exampleSigner struct{ priv ed25519.PrivateKey }

func (s exampleSigner) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

func (s exampleSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, msg), nil
}

// Example issues a root envelope, verifies it against the pinned key, and
// shows Narrow refusing a child that adds a root.
func Example() {
	ctl := exampleSigner{priv: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root := envelope.Envelope{
		Version:   envelope.Version,
		ID:        "root",
		Principal: "principal-1",
		Roots:     []string{"/work"},
		Tools:     []string{"read"},
		IssuedAt:  start,
		ExpiresAt: start.Add(time.Hour),
	}
	signed, err := envelope.Issue(context.Background(), ctl, root)
	if err != nil {
		fmt.Println(err)
		return
	}
	env, err := envelope.Verify(signed, ctl.PublicKey(), start.Add(time.Minute))
	fmt.Println("verified:", err == nil, env.ID)

	child := env
	child.ID = "child"
	child.ParentDigest, _ = envelope.Digest(env)
	child.Roots = []string{"/etc", "/work"}
	_, err = envelope.Narrow(env, child)
	var ne *envelope.NarrowError
	fmt.Println("narrow refused:", errors.As(err, &ne), ne.Field)
	// Output:
	// verified: true root
	// narrow refused: true roots
}
