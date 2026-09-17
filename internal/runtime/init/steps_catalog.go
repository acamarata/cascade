package init

// Purpose: wizard steps 4-5 (P1-E16-W4-S35-T6) — the plugin catalog
//   checklist and the provider intake loop.
// Constraints: the catalog is the LIVE registry, never a list written
//   here — a checklist offering a plugin this build cannot install would
//   install nothing and report success. The provider loop calls the real
//   `cascade provider add` intake; this package has no provider code of
//   its own.
// SPORT: internal/runtime/init steps 4-5 (ADD) — P1-E16-W4-S35-T6.

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// maxProviders bounds step 5's "add another?" loop.
//
// Not a policy on how many providers anyone may have — it is a guard on
// a loop whose exit condition is an answer from a Prompter, so that a
// Prompter which always says yes terminates instead of running forever.
const maxProviders = 32

// stepPlugins is step 4.
func (w *Wizard) stepPlugins(_ context.Context, state *State) error {
	w.say("== plugins")
	entries := w.deps.Catalog.Entries()
	if len(entries) == 0 {
		// Not an error: a build with no plugins registered is strange
		// but installable, and inventing rows would be worse.
		w.say("   this build registers no plugins")
		return nil
	}
	// STATED, NOT ASKED (R-14.277). The catalog IS the builtin registry:
	// every entry is compiled into this binary, its command namespace is
	// mounted unconditionally by the composition root, and nothing
	// anywhere reads an enabled flag for one. The step used to prompt
	// "Enable <name>?" for each and then use the answer for the summary
	// card and nothing else — a No changed nothing, and the plugin still
	// ran. A question whose answer is discarded is worse than no
	// question: it tells an operator they made a choice.
	//
	// `cascade plugin list` shows the same set marked builtin, and
	// `plugin enable|disable` refuses one with that fact, so all three
	// surfaces now say the same thing.
	selected := make([]string, 0, len(entries))
	for _, e := range entries {
		selected = append(selected, e.Name)
		w.say("   [built in] %-12s %s", e.Name, e.Description)
	}
	if spec := w.opts.Spec; spec != nil && spec.PluginsSet {
		// A setup file may still NAME plugins; it is told plainly that
		// the naming selects nothing here, rather than having its key
		// silently ignored or the whole file refused over it. The same
		// key does real work on the reconverge path, where the subject
		// is INSTALLED plugins.
		w.say("   this file names plugins, and they are all built in: there is nothing to select")
	}
	state.Plugins = selected
	return nil
}

// stepProviders is step 5.
//
// Every provider is added by `cascade provider add`, run as a real child
// process. That subcommand owns the credential read, the OAuth browser
// flow, the shape probe and the live micro-verify; this wizard holds no
// credential at any point and has no second intake path. A provider that
// `provider add` refuses is refused here too, by its own exit status.
func (w *Wizard) stepProviders(ctx context.Context, state *State) error {
	w.say("== providers")
	if spec := w.opts.Spec; spec != nil && spec.ProvidersSet {
		return w.specProviders(ctx, state, spec)
	}
	if !w.interactive() {
		// A non-interactive run configures no providers. Adding one
		// needs a credential or a browser, and a mode whose contract is
		// "answer for the operator" can obtain neither. Saying so is the
		// honest default; adding a provider without one would mean
		// inventing it, and reporting a provider that does not work is
		// worse than reporting none.
		w.say("   none (a non-interactive run configures no providers; " +
			"run `cascade provider add` when you have a credential)")
		return nil
	}
	added := make([]string, 0, 2)
	for i := 0; i < maxProviders; i++ {
		more, err := w.deps.Prompt.Confirm("Add a provider?", i == 0)
		if err != nil {
			return err
		}
		if !more {
			break
		}
		name, err := w.deps.Prompt.Line("Provider name", "")
		if err != nil {
			return err
		}
		if name == "" {
			return cascade.New(cascade.KindInvalidInput, "cascade init: a provider needs a name")
		}
		mode, err := w.deps.Prompt.Choose("How does it authenticate?", credentialModes(), CredentialOAuth)
		if err != nil {
			return err
		}
		flag, ok := credentialFlag(mode)
		if !ok {
			return cascade.Newf(cascade.KindInvalidInput,
				"cascade init: %q is not a credential mode; choose one of %v", mode, credentialModes())
		}
		if err := w.deps.Sub.Run(ctx, "provider", "add", name, flag); err != nil {
			// `provider add`'s own failure is returned as-is. It already
			// says what went wrong — a rejected key, a shape probe that
			// matched nothing, a verify that came back 401 — and
			// re-wording it here would only lose that.
			return cascade.Wrapf(cascade.KindUnavailable, err, "cascade init: `cascade provider add %s`", name)
		}
		added = append(added, name)
		w.say("   added %s", name)
	}
	state.Providers = added
	if len(added) == 0 {
		w.say("   none")
	}
	return nil
}

// The credential modes `cascade provider add` accepts, in the order the
// prompt offers them.
const (
	// CredentialOAuth is the browser flow, and the default: it is the
	// path that never puts a secret on a terminal.
	CredentialOAuth = "oauth"
	// CredentialKey reads a key from the terminal, with the subcommand's
	// own no-echo handling.
	CredentialKey = "key"
	// CredentialKeyEnv takes the key from a named environment variable.
	CredentialKeyEnv = "key-env"
)

// credentialModes returns the modes in presentation order.
func credentialModes() []string { return []string{CredentialOAuth, CredentialKey, CredentialKeyEnv} }

// credentialFlag maps a mode onto `provider add`'s own flag.
//
// The mapping is here, in one place, rather than built by string
// concatenation at the call site: the flag names belong to that
// subcommand, and a typo in a concatenated flag would surface as an
// unhelpful usage error from a child process rather than as a refusal
// naming the mode.
func credentialFlag(mode string) (string, bool) {
	switch mode {
	case CredentialOAuth:
		return "--oauth", true
	case CredentialKey:
		return "--key", true
	case CredentialKeyEnv:
		return "--key-env", true
	default:
		return "", false
	}
}

// specProviders runs the [[providers]] directives a setup file supplied.
//
// The directive names an environment VARIABLE, never a key. The value is
// read here, at the moment the provider is added, and handed straight to
// `cascade provider add --key-env`; it never enters the Spec, the journal
// or a log line. A variable that is unset is a refusal naming the
// variable — a provider added with an empty key fails later, somewhere
// that cannot say which setup file sent it.
func (w *Wizard) specProviders(ctx context.Context, state *State, spec *initconfig.Spec) error {
	added := make([]string, 0, len(spec.Providers))
	for _, directive := range spec.Providers {
		if _, err := directive.KeyFor(w.deps.Getenv); err != nil {
			return err
		}
		if !w.writing() {
			w.plan("add provider %s through `cascade provider add`", directive)
			added = append(added, directive.Name)
			continue
		}
		if err := w.deps.Sub.Run(ctx, providerAddArgs(directive)...); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err,
				"cascade init: `cascade provider add %s`", directive.Name)
		}
		added = append(added, directive.Name)
		w.say("   added %s", directive.Name)
	}
	state.Providers = added
	if len(added) == 0 {
		w.say("   none")
	}
	return nil
}

// providerAddArgs builds the subcommand invocation for one directive.
//
// Composed in one place rather than at the call site so the flag names —
// which belong to `cascade provider add`, not to this package — are
// written once. A typo in a concatenated flag surfaces as an unhelpful
// usage error from a child process.
func providerAddArgs(d initconfig.ProviderDirective) []string {
	args := []string{"provider", "add", d.Name, "--key-env", d.KeyEnv}
	if d.Kind != "" {
		args = append(args, "--kind", d.Kind)
	}
	if d.BaseURL != "" {
		args = append(args, "--base-url", d.BaseURL)
	}
	if !d.Verify {
		args = append(args, "--no-verify")
	}
	return args
}
