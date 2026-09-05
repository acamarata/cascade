// Package policy (explain.go): Purpose: the explain-why renderer. It turns
//
//	the layer results ONE evaluation produced into a deterministic,
//	human-readable trace, and it is the only place that text is built.
//
// Inputs: the LayerResults a run recorded, in the order they ran.
// Outputs: a Trace carrying those results, the deciding layer's rule and
//
//	the rendered explanation.
//
// Constraints: the renderer reads the run's own record and derives nothing
//
//	of its own, so an explanation that disagrees with its verdict is not a
//	state this package can reach. Rendering is a fixed walk over an
//	ordered slice with no map iteration anywhere, so identical inputs
//	produce byte-identical output. Nothing here reads a clock (Art.7.3);
//	an evaluation's time belongs to the audit record, not to its
//	explanation.
//
// SPORT: internal/policy explain-why-trace/ADDED (P1-E09-W2-S17-T2).
package policy

import "strconv"

// traceOf builds the Trace for one evaluation from the layer results it
// recorded. The slice is COPIED: a trace handed to a caller must not
// change when the run that produced it appends another layer.
func traceOf(results []LayerResult) Trace {
	layers := make([]LayerResult, len(results))
	copy(layers, results)
	t := Trace{Layers: layers, MatchedRule: matchedRule(layers)}
	t.Explanation = ExplainTrace(t)
	return t
}

// matchedRule names the layer that produced the FINAL verdict. Almost
// every trace has exactly one deciding layer; the one case with two is an
// ask the approval queue then refused, and there the answer the caller
// received is the later one. A trace with no deciding layer names none
// rather than borrowing the last layer's name, because a borrowed name
// would read as a decision that was never made.
func matchedRule(layers []LayerResult) string {
	name := ""
	for _, l := range layers {
		if l.Decided {
			name = l.Layer.String()
		}
	}
	return name
}

// ExplainTrace renders the layer-by-layer decision path of one evaluation.
//
// Each line names the layer's index, its stable name, the verdict it
// yielded and the rule that matched or the reason nothing did. The line
// for the layer that decided is marked, so a reader can see BOTH the
// answer and the layers that were consulted before it. This is the text
// `cascade policy explain` prints.
func ExplainTrace(t Trace) string {
	if len(t.Layers) == 0 {
		return "no policy layer ran, so nothing was decided"
	}
	out := make([]byte, 0, 128*len(t.Layers))
	for i, l := range t.Layers {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, explainLayer(l)...)
	}
	return string(out)
}

// explainLayer renders one layer's line.
func explainLayer(l LayerResult) string {
	line := "layer " + strconv.Itoa(int(l.Index)) + " (" + l.Layer.String() + "): "
	if !l.Decided {
		return line + "continued, because " + ruleText(l)
	}
	return line + "DECIDED " + safeVerdict(l.Verdict).String() + ", because " + ruleText(l)
}

// ruleText renders a layer's rule, naming the absence rather than printing
// an empty clause when a layer recorded none.
func ruleText(l LayerResult) string {
	if l.Rule == "" {
		return "this layer recorded no rule"
	}
	return l.Rule
}
