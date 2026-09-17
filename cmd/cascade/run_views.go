// Purpose: the human rendering of `cascade run`'s result.
//
// The model's OUTPUT is the point of this verb, so it leads and is not
// decorated. What follows it is the accounting an operator checks: which
// job, what it cost, and — for a multi-leg run — the same per leg.
//
// SPORT: cmd/cascade/run view (ADD) — P1-E19-W4-S42-T7.
package main

import (
	"bytes"
	"fmt"

	"github.com/acamarata/cascade/pkg/provider"
)

// String renders one run.
func (v runResultView) String() string {
	var buf bytes.Buffer
	buf.WriteString(v.Output)
	if v.Output != "" && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		buf.WriteString("\n")
	}
	_, _ = fmt.Fprintf(&buf, "\njob %s, %s\n", orNone(v.JobID), usageLine(v.Cost))
	for i, leg := range v.Legs {
		_, _ = fmt.Fprintf(&buf, "  leg %d: job %s, %s\n", i+1, orNone(leg.JobID), usageLine(leg.Cost))
	}
	return buf.String()
}

// usageLine renders token accounting compactly.
func usageLine(u provider.Usage) string {
	return fmt.Sprintf("%d in / %d out tokens", u.InputTokens, u.OutputTokens)
}
