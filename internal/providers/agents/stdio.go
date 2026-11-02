// Purpose: the output filter the host shim binds to every driver spawn: a
// value-based scrub of every secret injected into that child, then the
// process egress engine's substitution pass for class agent-driver.
// Inputs: the process egress engine, the spawn's data tier, the literal
// secrets BuildDriverEnv returned (BuiltEnv.Secrets) and a content-free
// failure callback.
// Outputs: a provider.OutputFilter. A chunk comes out with every literal
// (raw, base64 and hex spellings) replaced by RedactedTag and then
// substituted by the engine.
// Constraints: fail closed: any engine error withholds the chunk, returns
// provider.ErrEgressSubstitutionFailed by identity and calls fail once with a
// reason that carries no content; a nil engine is the same refusal; no
// second detector or redactor lives here, the engine owns the pattern pass.
// A literal split across two chunks is not seen by the scrub (drivers hand
// over whole lines); the engine's detector still sees each chunk.
// SPORT: internal.providers.agents/ADD (P1-AGT-03, contract:driver-custody-shim).

package agents

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RedactedTag replaces every occurrence of an injected secret value.
const RedactedTag = "[REDACTED:agent-driver-secret]"

// minEncodedLen is the shortest encoded fragment worth scrubbing; anything
// shorter would match unrelated text.
const minEncodedLen = 6

// WrapDriverOutput returns the filter for one spawn. See the file comment.
func WrapDriverOutput(engine *egress.Engine, tier provider.SensitivityTier, literals [][]byte, fail func(reason string)) provider.OutputFilter {
	needles := scrubNeedles(literals)
	return func(chunk []byte) ([]byte, error) {
		scrubbed := chunk
		for _, n := range needles {
			scrubbed = bytes.ReplaceAll(scrubbed, n, []byte(RedactedTag))
		}
		var out []byte
		var err error
		if engine != nil {
			out, err = engine.InterceptClass(context.Background(), egress.EgressClassAgentDriver, tier, scrubbed)
		}
		if engine == nil || err != nil {
			if fail != nil {
				fail("agent-driver output substitution failed; the chunk was withheld")
			}
			return nil, provider.ErrEgressSubstitutionFailed
		}
		return out, nil
	}
}

// scrubNeedles returns every spelling of every literal, longest first so a
// short literal never splits a longer one before it is replaced.
func scrubNeedles(literals [][]byte) [][]byte {
	seen := map[string]bool{}
	var out [][]byte
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, []byte(s))
		}
	}
	for _, lit := range literals {
		if len(lit) == 0 {
			continue
		}
		add(string(lit))
		hx := hex.EncodeToString(lit)
		add(hx)
		add(strings.ToUpper(hx))
		for _, s := range base64Fragments(lit) {
			add(s)
		}
	}
	slices.SortStableFunc(out, func(a, b []byte) int { return len(b) - len(a) })
	return out
}

// base64Fragments returns the base64 text a literal produces at each of the
// three byte alignments it can take inside a larger encoded stream, in the
// standard and URL alphabets, with the characters that depend on neighbouring
// bytes trimmed so the fragment matches wherever the literal was embedded.
func base64Fragments(lit []byte) []string {
	var out []string
	skip := [3]int{0, 2, 3}
	for shift := 0; shift < 3; shift++ {
		padded := append(make([]byte, shift), lit...)
		enc := base64.RawStdEncoding.EncodeToString(padded)
		if rem := len(padded) % 3; rem != 0 && len(enc) > 0 {
			enc = enc[:len(enc)-1]
		}
		if len(enc) <= skip[shift] {
			continue
		}
		frag := enc[skip[shift]:]
		if len(frag) < minEncodedLen {
			continue
		}
		out = append(out, frag, strings.NewReplacer("+", "-", "/", "_").Replace(frag))
	}
	return out
}

// bindFilter binds the stdio filter at the joined tier of the envelope and the job.
func (r *spawnRun) bindFilter(context.Context) error {
	engine, err := r.s.d.Engine()
	if err != nil {
		return err
	}
	if engine == nil {
		return cascade.New(cascade.KindUnavailable, "agents: the process egress engine is not available")
	}
	tier := provider.JoinSensitivity(r.job.Envelope.Sensitivity, r.job.DataClass)
	harness := string(r.b.Harness)
	r.job.Filter = WrapDriverOutput(engine, tier, r.built.Secrets, func(reason string) {
		slog.Warn("agent driver output withheld", "harness", harness, "job", string(r.job.JobID), "reason", reason)
	})
	return nil
}
