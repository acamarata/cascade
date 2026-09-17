// Purpose: the human rendering of every `cascade vault …` result.
//
// Each of these types is NAMED a view and had no String method, so
// output.Writer.Result printed Go's default struct formatting: the
// convention was assumed and never enforced. The W-4 hardening gate's
// result-stringer check now enforces it (R-14.277).
//
// Constraints: NO SECRET VALUE APPEARS HERE. These verbs are about secret
//
//	material, and a rendering that echoed a value would put it in a
//	terminal scrollback, a CI log and a screen share at once. Every view
//	below names the secret and says what happened to it.
//
// SPORT: cmd/cascade/vault views (ADD) — P1-E19-W4-S42-T7.
package main

import (
	"bytes"
	"fmt"
	"text/tabwriter"
)

// String says which secret was stored, and whether it replaced one.
func (v setResultView) String() string {
	verb := "stored"
	if v.Replaced {
		verb = "replaced"
	}
	return fmt.Sprintf("%s %s in the %s vault", verb, v.Name, v.Backend)
}

// String says which secret was rotated.
func (v rotateResultView) String() string {
	if !v.Rotated {
		return fmt.Sprintf("%s was NOT rotated", v.Name)
	}
	return fmt.Sprintf("rotated %s in the %s vault", v.Name, v.Backend)
}

// String summarises an import, then names what landed.
//
// The names and not the values: an import file's whole content is secret
// material, and an operator needs to know which keys arrived.
func (v importResultView) String() string {
	head := fmt.Sprintf("parsed %d, created %d, updated %d in the %s vault",
		v.Parsed, v.Created, v.Updated, v.Backend)
	if len(v.Names) == 0 {
		return head
	}
	return head + "\n  " + joinOrNone(v.Names)
}

// String renders the quarantine.
//
// The matched TEXT is never shown — only where it was found and what it
// looked like. A quarantine listing that printed the secret would defeat
// the quarantine.
func (v quarantineListView) String() string {
	if len(v.Entries) == 0 {
		return "the quarantine is empty"
	}
	var buf bytes.Buffer
	_, _ = fmt.Fprintf(&buf, "%d pending\n\n", v.Pending)
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tCLASS\tPATTERN\tCONFIDENCE\tSUGGESTED NAME\tSOURCE\tDETECTED")
	for _, e := range v.Entries {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t%s\t%s\t%s\n",
			e.ID, e.Class, e.Pattern, e.Confidence, orNone(e.SuggestedName), orNone(e.SourceRef), e.DetectedAt)
	}
	_ = tw.Flush()
	return buf.String()
}

// String says which entry was released and why.
func (v releaseView) String() string {
	return fmt.Sprintf("released quarantine entry %s: %s", v.ID, orNone(v.Reason))
}

// String says where a quarantined value landed.
func (v promoteView) String() string {
	verb := "stored"
	if v.Replaced {
		verb = "replaced"
	}
	return fmt.Sprintf("%s %s in the %s vault from quarantine entry %s",
		verb, v.Name, v.Backend, v.QuarantineID)
}
