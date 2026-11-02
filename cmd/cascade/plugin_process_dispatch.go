// Purpose: dispatch a declared process-plugin CLI verb to the plugin's
//
//	declared rpc_method through a live, trusted handle, and render the
//	result through the output writer.
//
// Inputs: the cobra command and positional args of a generic process verb,
//
//	and its processPluginCommand (plugin id + declared CommandSpec).
//
// Outputs: contract:process-verb-envelope. The call params are
//
//	{"args": [<positional strings>], "flags": {<changed flag>: <string>}};
//	the result is rendered as untrusted-source output (contract:
//	untrusted-provenance): the --json envelope's data carries
//	"provenance": "untrusted-source" next to the plugin id and the plugin's
//	result, and human output prints it under an "untrusted plugin output"
//	header. A plugin result is attacker-reachable text (R14 F-7).
//
// Constraints: the plugin is reached ONLY through plugins.LaunchInstalled
//
//	(the TrustedHandle path, which records an intent and a terminal effect
//	record per call); process.Handle is never called directly. A refusal to
//	launch (missing, untrusted, disabled or digest-mismatched record) is a
//	KindUnavailable error naming `cascade plugin add <plugin>`; nothing
//	starts. A plugin error reply is returned with its own typed Kind and no
//	result is fabricated. The handle is closed before the command returns.
//
// SPORT: cmd/cascade:plugin-process-dispatch (ADD) — P1-PLG-04.
package main

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
)

// processVerbLauncher is the launch seam; production is LaunchInstalled.
var processVerbLauncher = plugins.LaunchInstalled

// provenanceUntrustedSource is the label every dispatched result carries.
const provenanceUntrustedSource = "untrusted-source"

// processVerbFlagNames are the string flags a generic process verb accepts.
// Only the ones the user changed travel in the envelope.
var processVerbFlagNames = []string{
	"state", "title", "body", "head", "base", "merge-method",
	"label", "reviewers", "team-reviewers", "draft", "per-page",
}

// processVerbOutput is the data member of a dispatched verb's envelope.
type processVerbOutput struct {
	Provenance string          `json:"provenance"`
	Plugin     string          `json:"plugin"`
	Result     json.RawMessage `json:"result"`
}

// String is the human form: the untrusted header, then the result.
func (o processVerbOutput) String() string {
	return "untrusted plugin output (" + o.Plugin + "): " + string(o.Result)
}

// errProcessPluginUnreachable is the typed refusal a process-runtime verb
// returns when its plugin cannot be launched: not installed, disabled, not
// trusted, or no longer matching its trusted digest. It names the remedy.
func errProcessPluginUnreachable(pluginID, commandPath string, cause error) error {
	return cascade.Wrapf(cascade.KindUnavailable, cause,
		"%s: the %s plugin is not an enabled, trusted plugin on this machine, so nothing was launched; "+
			"run `cascade plugin add %s` to install and trust it", commandPath, pluginID, pluginID)
}

// addProcessVerbFlags declares the generic verb's string flags.
func addProcessVerbFlags(cmd *cobra.Command) {
	for _, name := range processVerbFlagNames {
		cmd.Flags().String(name, "", "passed to the plugin verb as --"+name)
	}
}

// processVerbEnvelope builds the {"args", "flags"} call params.
func processVerbEnvelope(cmd *cobra.Command, args []string) ([]byte, error) {
	flags := map[string]string{}
	for _, name := range processVerbFlagNames {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			flags[name] = f.Value.String()
		}
	}
	if args == nil {
		args = []string{}
	}
	raw, err := json.Marshal(struct {
		Args  []string          `json:"args"`
		Flags map[string]string `json:"flags"`
	}{args, flags})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "encoding the verb envelope")
	}
	return raw, nil
}

// dispatchProcessVerb launches c's plugin, calls its declared rpc_method
// with the envelope, renders the result and closes the handle.
func dispatchProcessVerb(cmd *cobra.Command, args []string, c processPluginCommand) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if c.Spec.RPCMethod == "" {
		return cascade.Newf(cascade.KindUnsupported,
			"%s: the %s plugin declares no rpc_method for this verb", cmd.CommandPath(), c.PluginID)
	}
	params, err := processVerbEnvelope(cmd, args)
	if err != nil {
		return err
	}
	h, err := processVerbLauncher(ctx, c.PluginID)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) || cascade.HasKind(err, cascade.KindPolicyDenied) {
			return errProcessPluginUnreachable(c.PluginID, cmd.CommandPath(), err)
		}
		return err
	}
	result, callErr := h.Call(ctx, c.Spec.RPCMethod, params)
	closeErr := h.Close(context.WithoutCancel(ctx))
	if callErr != nil {
		return callErr
	}
	if closeErr != nil {
		return closeErr
	}
	return renderProcessVerb(cmd, c.PluginID, result)
}

// renderProcessVerb writes result as labelled untrusted output.
func renderProcessVerb(cmd *cobra.Command, pluginID string, result []byte) error {
	// UseNumber keeps integers past 2^53 exact in the human form.
	dec := json.NewDecoder(bytes.NewReader(result))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "%s: the plugin's result is not JSON", pluginID)
	}
	if dec.More() {
		return cascade.Newf(cascade.KindInternal, "%s: the plugin's result is not JSON (trailing data)", pluginID)
	}
	w := outputWriter(cmd)
	if w.Mode().JSON {
		return w.Result(processVerbOutput{Provenance: provenanceUntrustedSource, Plugin: pluginID, Result: result})
	}
	return printUntrustedHuman(w, pluginID, decoded)
}

// printUntrustedHuman prints decoded under the untrusted header. It
// re-encodes the decoded value, so control characters are escaped.
func printUntrustedHuman(w *output.Writer, pluginID string, decoded any) error {
	body, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "rendering the plugin result")
	}
	w.Println("untrusted plugin output (" + pluginID + "):")
	w.Println(string(body))
	return nil
}
