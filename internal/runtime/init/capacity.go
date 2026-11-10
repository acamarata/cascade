package init

// Purpose: the AI-capacity checklist step (P1-NODE-12c). It prints what
//   the provider intake already holds as a `detected: ...` checklist and
//   asks whether the operator has additional accounts to add.
// Inputs: the wizard's Prompter, Subprocess and CurrentState, plus the
//   intake Registry the records are read back from.
// Outputs: State.Providers (names only) and the rendered checklist.
// Constraints: there is no second intake path. Every additional account
//   is added by `cascade provider add`, run as a real child process,
//   exactly as step 5 does. This step never holds a credential: what it
//   reads back and prints is the record's vault reference, and a record
//   whose reference is not one is refused before anything is printed.
// SPORT: internal/runtime/init capacity step (ADD) — P1-NODE-12c.

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The capacity step's prompt wording. One place, so the spec prompter
// and the tests can never drift from what the step actually asks.
const (
	capacityMoreQuestion = "Do you have additional AI accounts to add?"
	capacityNameQuestion = "Account name"
	capacityModeQuestion = "How does it authenticate?"
	capacityPoolQuestion = "Key-pool to join (blank for standalone)"
)

// gatewayRefPrefix marks a gateway-account reference ("gateway:<acct>"),
// which the intake records for AuthGateway instead of a vault key.
const gatewayRefPrefix = "gateway:"

// StepCapacity runs the AI-capacity checklist.
//
// It renders one `detected: ...` line per provider the intake registry
// already holds, then asks the additional-accounts question. Each yes
// runs `cascade provider add`, reads the new record back from reg and
// prints its vault reference. A non-writing run plans the add instead.
func (w *Wizard) StepCapacity(ctx context.Context, state *State, reg intake.Registry) error {
	if reg == nil || w.deps.Prompt == nil || w.deps.Sub == nil {
		return cascade.New(cascade.KindInvalidInput,
			"cascade init: the capacity step needs the provider registry, a prompter and a subprocess runner")
	}
	w.say("== capacity")
	names, err := w.installedProviders(ctx, state)
	if err != nil {
		return err
	}
	detected := make([]string, 0, len(names))
	for _, name := range names {
		rec, err := reg.GetProvider(ctx, name)
		if err != nil {
			return err
		}
		if err := w.sayCapacityRecord("detected", rec); err != nil {
			return err
		}
		detected = append(detected, rec.Name)
	}
	if len(detected) == 0 {
		w.say("   detected: none")
	}
	added, err := w.addCapacityAccounts(ctx, reg)
	if err != nil {
		return err
	}
	state.Providers = mergeNames(detected, added)
	return nil
}

// installedProviders lists the provider names the checklist covers: the
// installed set when the machine can report it, else what this run's
// journal already recorded.
func (w *Wizard) installedProviders(ctx context.Context, state *State) ([]string, error) {
	if w.deps.Current == nil {
		return append([]string(nil), state.Providers...), nil
	}
	names, err := w.deps.Current.Providers(ctx)
	if err != nil {
		return nil, err
	}
	return mergeNames(names, state.Providers), nil
}

// addCapacityAccounts asks the additional-accounts question until the
// operator says no, adding each account through the one intake path.
func (w *Wizard) addCapacityAccounts(ctx context.Context, reg intake.Registry) ([]string, error) {
	added := make([]string, 0, 2)
	for i := 0; i < maxProviders; i++ {
		more, err := w.deps.Prompt.Confirm(capacityMoreQuestion, false)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
		args, name, err := w.capacityAddArgs()
		if err != nil {
			return nil, err
		}
		if !w.writing() {
			w.plan("add account %s through `cascade provider add`", name)
			added = append(added, name)
			continue
		}
		if err := w.deps.Sub.Run(ctx, args...); err != nil {
			return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade init: `cascade provider add %s`", name)
		}
		rec, err := reg.GetProvider(ctx, name)
		if err != nil {
			return nil, err
		}
		if err := w.sayCapacityRecord("added", rec); err != nil {
			return nil, err
		}
		added = append(added, name)
	}
	return added, nil
}

// capacityAddArgs asks for one account's name, credential mode and pool
// and builds the `cascade provider add` invocation from them. No answer
// here is a credential: the mode only selects which flag the child
// process reads its credential through.
func (w *Wizard) capacityAddArgs() ([]string, string, error) {
	name, err := w.deps.Prompt.Line(capacityNameQuestion, "")
	if err != nil {
		return nil, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", cascade.New(cascade.KindInvalidInput, "cascade init: an additional account needs a name")
	}
	if w.deps.Secrets != nil {
		if err := w.deps.Secrets.Check("account name", name); err != nil {
			return nil, "", err
		}
	}
	mode, err := w.deps.Prompt.Choose(capacityModeQuestion, credentialModes(), CredentialOAuth)
	if err != nil {
		return nil, "", err
	}
	flag, ok := credentialFlag(mode)
	if !ok {
		return nil, "", cascade.Newf(cascade.KindInvalidInput,
			"cascade init: %q is not a credential mode; choose one of %v", mode, credentialModes())
	}
	pool, err := w.deps.Prompt.Line(capacityPoolQuestion, "")
	if err != nil {
		return nil, "", err
	}
	args := []string{"provider", "add", name, flag}
	if pool = strings.TrimSpace(pool); pool != "" {
		args = append(args, "--pool", pool)
	}
	return args, name, nil
}

// sayCapacityRecord prints one checklist line, refusing a record whose
// reference is not a vault (or gateway) reference before printing it.
func (w *Wizard) sayCapacityRecord(verb string, rec intake.ProviderRecord) error {
	if err := checkVaultRef(rec); err != nil {
		return err
	}
	line := rec.Name + " (" + string(rec.Driver) + ", " + string(rec.Auth) + ", ref " + rec.AuthRef.String()
	if rec.Pool != "" {
		line += ", pool " + rec.Pool
	}
	w.say("   %s: %s)", verb, line)
	return nil
}

// checkVaultRef accepts only the reference shapes the intake mints:
// "provider.<name>.<suffix>" for a vault key, "gateway:<account>" for a
// gateway source. Anything else may be a value rather than a name, and
// is refused without being echoed.
func checkVaultRef(rec intake.ProviderRecord) error {
	ref := rec.AuthRef.String()
	vaultPrefix := "provider." + rec.Name + "."
	switch {
	case rec.Auth == intake.AuthGateway && strings.HasPrefix(ref, gatewayRefPrefix) && len(ref) > len(gatewayRefPrefix):
		return nil
	case rec.Auth != intake.AuthGateway && strings.HasPrefix(ref, vaultPrefix) && len(ref) > len(vaultPrefix):
		return nil
	}
	return cascade.Newf(cascade.KindIntegrity,
		"cascade init: provider %q carries a credential reference that is not a vault reference; it was not printed",
		rec.Name)
}

// mergeNames appends b's names to a's, skipping duplicates, keeping order.
func mergeNames(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, n := range list {
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
