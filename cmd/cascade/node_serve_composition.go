// Purpose: `cascade node serve`'s composition — resolving the data dir and
//   building every real backend the run loop needs. Split out of
//   node_serve.go under Art.10.3's 300-line cap; the run loop and the
//   wiring that feeds it are separate concerns, so this is the seam rather
//   than an arbitrary cut.
// SPORT: cmd/cascade/node-serve-composition (ADD, P1-E17-W4-S37-T2 split).

package main

import (
	"context"
	"crypto/rand"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// composeNodeServe resolves dataDir and builds every collaborator
// runNodeServe needs: the keystore, this node's local identity (generated
// on first run), the durable action log, and the real RPC registry.
func composeNodeServe(ctx context.Context, deps nodeServeDeps) (nodeServeComposition, error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nodeServeComposition{}, cascade.New(cascade.KindUnavailable, "node serve: could not resolve the data directory")
	}
	keystore, err := nodes.NewNodeKeystore(nodeKeystoreConfig(deps.SecretsDir, dataDir))
	if err != nil {
		return nodeServeComposition{}, cascade.Wrap(cascade.KindUnavailable, err, "node serve: open keystore")
	}
	self, err := nodes.EnsureLocalIdentity(ctx, nodes.NewFileSelfIdentityBackend(dataDir), keystore, rand.Reader)
	if err != nil {
		return nodeServeComposition{}, cascade.Wrap(cascade.KindUnavailable, err, "node serve: establish local identity")
	}
	actionLog, enrollmentID := nodeDispatchLeg(dataDir, self)
	recordStore := nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), deps.Clock)
	knownHosts := nodes.NewKnownHosts(nodes.NewFileKnownHostsBackend(dataDir))
	registry := nodes.NewServeRegistry(nodes.ServeDeps{
		Records:      recordStore,
		KnownHosts:   knownHosts,
		Keystore:     keystore,
		Self:         self,
		Sequences:    nodes.NewSequenceStore(),
		Clock:        deps.Clock,
		Timeout:      nodes.DefaultHeartbeatTimeout,
		Actions:      actionLog,
		EnrollmentID: enrollmentID,
	})
	return nodeServeComposition{
		dataDir: dataDir, keystore: keystore, self: self, registry: registry,
		recordStore: recordStore, knownHosts: knownHosts,
	}, nil
}

// nodeKeystoreConfig builds the secrets config every node command opens its
// keystore with.
//
// Dir is ALWAYS set, to the data directory when no test overrode it. That
// is the fix for a production refusal, not a tidy-up: SelectCustody falls
// back to the encrypted file vault when no platform backend is available,
// and the file vault refuses to construct without a directory — so with Dir
// empty, `cascade node serve` could not start at all on a headless Linux
// box, a locked keychain, or a CI runner. The node agent is the component
// most likely to run exactly there (R-14.270).
//
// ForceFileVault stays tied to the OVERRIDE alone. Setting Dir does not
// force the file vault — SelectCustody still prefers a working platform
// backend — so a real user's key still lands in their OS keychain, and only
// a test that deliberately named a directory opts out of it.
func nodeKeystoreConfig(secretsDir, dataDir string) secrets.Config {
	dir := secretsDir
	if dir == "" {
		dir = dataDir
	}
	return secrets.Config{Dir: dir, ForceFileVault: secretsDir != ""}
}
