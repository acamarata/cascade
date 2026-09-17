package main

// Purpose: the human rendering of `provider add` (P1-E16-W4-S35-T10).
// Inputs: the intake result the subcommand already produces.
// Outputs: one line per fact an operator needs after adding a provider.
// Constraints: output.Writer.Result calls fmt.Stringer and nothing else
//   (R-14.253 Finding 2), so a result handed over without a view prints
//   Go's default struct formatting. That is what `provider add` did. The
//   view EMBEDS the result rather than copying it, so --json keeps
//   emitting the same document and a new field cannot be silently
//   dropped from one surface.
// SPORT: cmd/cascade provider add view (ADD) — P1-E16-W4-S35-T10.

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/pkg/provider"
)

// providerAddView renders one completed intake.
type providerAddView struct {
	intake.AddResult
}

// String renders what the add did, as a labelled block.
//
// The credential column names the vault REFERENCE, never a value: the
// record carries a key name because that is the only credential-shaped
// thing intake keeps, and printing it is how an operator confirms the key
// went where they expect.
func (v providerAddView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	r := v.Record
	_, _ = fmt.Fprintf(tw, "provider\t%s\n", r.Name)
	_, _ = fmt.Fprintf(tw, "status\t%s\n", v.Status)
	_, _ = fmt.Fprintf(tw, "driver\t%s\n", r.Driver)
	_, _ = fmt.Fprintf(tw, "endpoint\t%s\n", r.BaseURL)
	_, _ = fmt.Fprintf(tw, "credential\t%s (%s)\n", r.AuthRef, r.Auth)
	_, _ = fmt.Fprintf(tw, "models\t%s\n", modelsSummary(r.KnownModels))
	_, _ = fmt.Fprintf(tw, "capabilities\t%s\n", capabilitiesSummary(r.Capabilities))
	_ = tw.Flush()

	out := strings.TrimRight(buf.String(), "\n")
	for _, w := range v.Warnings {
		out += "\nwarning: " + w
	}
	return out
}

// modelsSummary lists the enumerated models, or says plainly that none
// were.
//
// "none" rather than an empty column: a blank there reads as a rendering
// bug, and "the endpoint enumerated no models" is a real state an operator
// needs to see — it is the one micro-verify treats as a verify failure.
func modelsSummary(models []string) string {
	if len(models) == 0 {
		return "none enumerated"
	}
	if len(models) <= maxListedModels {
		return strings.Join(models, ", ")
	}
	return fmt.Sprintf("%s (+%d more)",
		strings.Join(models[:maxListedModels], ", "), len(models)-maxListedModels)
}

// maxListedModels caps the model column. A vendor listing two hundred
// models would otherwise push every other row off the screen, and the full
// list is one `--json` away.
const maxListedModels = 4

// capabilitiesSummary names the dimensions this lane is known to support.
//
// Only the supported ones, with "not probed" when nothing is known: the
// tri-state's whole point is that unknown and unsupported are different,
// and a row of six "unknown"s tells an operator less than one sentence
// saying the probe has not run.
func capabilitiesSummary(c provider.Capabilities) string {
	dims := []struct {
		name  string
		state provider.CapabilityState
	}{
		{"search", c.Search},
		{"url-fetch", c.URLFetch},
		{"vision", c.Vision},
		{"tool-use", c.ToolUse},
		{"long-context", c.LongContext},
		{"structured-output", c.StructuredOutput},
	}
	var supported []string
	probed := false
	for _, d := range dims {
		if d.state != provider.CapabilityUnknown {
			probed = true
		}
		if d.state == provider.CapabilitySupported {
			supported = append(supported, d.name)
		}
	}
	switch {
	case len(supported) > 0:
		return strings.Join(supported, ", ")
	case probed:
		return "none of the probed dimensions are supported"
	default:
		return "not probed"
	}
}
