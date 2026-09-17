// Purpose: `cascade sync`'s production composition — what the verbs reach
//   on a real machine.
// RECORDED GAP, not papered over: the peer trust tier and the run path
//   both come from a sync SESSION, and nothing in this tree opens one yet
//   (the transport is S-38.T1's, the two-machine round trip is S-38.T5's
//   and needs the owner's second machine). So production wires the
//   engine, the journal and the elevation gate — which are real and which
//   `status` and `conflicts list|resolve` are entirely built on — and
//   leaves Run nil, which makes `sync run` REPORT that it is not wired
//   rather than report a sync that never happened.
// SPORT: cli.sync deps (ADD) — P1-E17-W4-S38-T3.

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	syncpkg "github.com/acamarata/cascade/internal/sync"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/sqlite"
)

// productionSyncDeps builds syncDeps against the real environment.
//
// PeerTier is the controller's, because that is what this machine is when
// it runs the CLI: the tier in a status report is the tier of the peer the
// question is about, and asking locally asks about here.
// NOTHING HERE OPENS ANYTHING. The composition root builds every command
// tree at startup — including for `cascade init`, `cascade version` and
// `--help` — so a dependency that opened a database while being described
// would create ~/.cascade on a machine whose operator had only asked for
// the help text. It did: adding an os.MkdirAll here turned six unrelated
// tests red at once, all of them asserting that a refused or read-only
// command had written nothing.
func productionSyncDeps() syncDeps {
	return syncDeps{
		OpenEngine: func() (*syncpkg.Engine, io.Closer) {
			store := openSyncStore()
			closer, _ := store.(io.Closer)
			return syncpkg.NewEngine(store, runtime.NewSystemClock(), nil), closer
		},
		PeerTier: nodes.TierController,
		Getenv:   os.Getenv,
	}
}

// openSyncStore opens the cursor store `sync status` reads positions from.
//
// A store it cannot open is nil, and nil is REPORTED rather than treated
// as "position zero": the cursor store refuses a read it cannot make, and
// status renders the position as unknown. Zero is a real position — a
// domain that has never synced is at zero — so printing it for a database
// nobody could open would be a number an operator would believe.
//
// IT RETURNS THE INTERFACE, NOT THE CONCRETE TYPE, and that is the whole
// reason this is a named function. Returning `*sqlite.Driver` and handing
// it to something expecting a provider.Store puts a TYPED nil in the
// interface: the value is nil, the interface is not, every `== nil` guard
// downstream passes, and the first method call panics. It did, before this
// was written this way.
func openSyncStore() provider.Store {
	paths, err := runtime.NewPathProvider(os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil
	}
	dataDir := paths.DataDir()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(dataDir, "cascade.db"))
	if err != nil {
		return nil
	}
	return store
}
