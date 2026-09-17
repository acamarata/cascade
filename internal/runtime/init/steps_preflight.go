package init

// Purpose: wizard steps 1-3 (P1-E16-W4-S35-T6) — preflight, profile and
//   storage.
// Constraints: step 2's worker branch hands off to `cascade node enroll`
//   as a real subprocess and stops; step 3 refuses a literal secret in
//   any prompt rather than storing one.
// SPORT: internal/runtime/init steps 1-3 (ADD) — P1-E16-W4-S35-T6.

import (
	"context"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The three profiles.
const (
	ProfileLocal  = "local"
	ProfileServer = "server"
	ProfileWorker = "worker"
)

// profiles returns the selectable profiles in their presentation order.
func profiles() []string { return []string{ProfileLocal, ProfileServer, ProfileWorker} }

// stepPreflight is step 1: report what is already here, and prove the
// storage location is usable before anything is written to it.
//
// The probe runs FIRST, before any prompt. A wizard that asked nine
// questions and then failed on an unwritable home would have wasted the
// operator's time on a machine it could have refused immediately.
func (w *Wizard) stepPreflight(ctx context.Context, state *State) error {
	w.say("== preflight")
	if _, err := os.Stat(w.deps.Home); err == nil {
		w.say("   %s already exists; resuming rather than starting over", w.deps.Home)
	} else {
		w.say("   %s does not exist yet", w.deps.Home)
	}
	if err := w.deps.Storage.Probe(ctx, w.deps.Home, w.writing()); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err,
			"cascade init: the cascade home is not usable; nothing was changed")
	}
	if state.CompletedStep > 0 {
		w.say("   last completed step: %s", state.CompletedStep)
	}
	return nil
}

// stepProfile is step 2.
//
// The worker branch execs `cascade node enroll` and STOPS. The
// controller-endpoint prompt belongs to that subcommand; asking it here
// too would be a second implementation of one question, and the two would
// drift.
func (w *Wizard) stepProfile(ctx context.Context, state *State) error {
	w.say("== profile")
	chosen := w.opts.Profile
	if chosen == "" {
		picked, err := w.deps.Prompt.Choose("Which profile?", profiles(), ProfileLocal)
		if err != nil {
			return err
		}
		chosen = picked
	}
	if !validProfile(chosen) {
		return cascade.Newf(cascade.KindInvalidInput,
			"cascade init: %q is not a profile; choose one of %v", chosen, profiles())
	}
	state.Profile = chosen
	w.say("   profile: %s", chosen)

	if chosen != ProfileWorker {
		return nil
	}
	if !w.writing() {
		w.plan("hand off to `cascade node enroll` and stop, because a worker is configured by its controller")
		return nil
	}
	w.say("   a worker is enrolled by its controller; handing off to `cascade node enroll`")
	if err := w.deps.Sub.Run(ctx, "node", "enroll"); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade init: `cascade node enroll` failed")
	}
	return nil
}

// validProfile reports whether name is one of the three.
func validProfile(name string) bool {
	for _, p := range profiles() {
		if p == name {
			return true
		}
	}
	return false
}

// stepStorage is step 3.
//
// The local profile confirms one path and is done — zero configuration is
// the point of that profile. The server profile asks for three connection
// references, and every answer goes through the literal-secret guard: a
// connection string with a password in it typed at this prompt would land
// in config.toml in plaintext, which is the single most likely way this
// wizard could leak a credential.
func (w *Wizard) stepStorage(_ context.Context, state *State) error {
	w.say("== storage")
	if state.Profile == ProfileServer {
		return w.serverStorage(state)
	}
	// The composition root's own path, not one recomputed here. A second
	// derivation of the layout is how this step came to advertise
	// Home/cascade.db while every subsystem opened DataDir()/cascade.db,
	// and how its probe left an empty database at the advertised path
	// (R-14.279).
	def := w.deps.LocalDBPath
	if !w.interactive() {
		// The local database's location is DERIVED from the cascade
		// home, not chosen. The prompt exists so a person at a terminal
		// can relocate it; a run with nobody there has nothing to
		// decide, and asking would turn a computed path into a question
		// CASCADE_NO_INPUT then has to refuse. A value the wizard
		// computed is not an answer it assumed on somebody's behalf.
		state.StoragePath = def
		w.say("   sqlite: %s", def)
		return nil
	}
	path, err := w.deps.Prompt.Line("Where should the local database live?", def)
	if err != nil {
		return err
	}
	if path == "" {
		path = def
	}
	state.StoragePath = path
	w.say("   sqlite: %s", path)
	return nil
}

// serverStoragePrompts are the three references the server profile needs,
// in a fixed order so a resumed run asks them in the same sequence.
func serverStoragePrompts() []struct{ field, question string } {
	return []struct{ field, question string }{
		{"postgres", "Postgres connection, as a vault reference"},
		{"redis", "Redis connection, as a vault reference"},
		{"s3", "S3 endpoint, as a vault reference"},
	}
}

// serverStorage asks for the three references and refuses any literal.
func (w *Wizard) serverStorage(state *State) error {
	refs := make([]string, 0, len(serverStoragePrompts()))
	for _, p := range serverStoragePrompts() {
		value, err := w.deps.Prompt.Line(p.question, "")
		if err != nil {
			return err
		}
		if value == "" {
			continue
		}
		if err := w.deps.Secrets.Check(p.field, value); err != nil {
			return err
		}
		refs = append(refs, p.field+"="+value)
		w.say("   %s: %s", p.field, value)
	}
	state.StoragePath = joinRefs(refs)
	return nil
}

// joinRefs renders the server profile's references for the journal.
func joinRefs(refs []string) string {
	out := ""
	for i, r := range refs {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out
}
