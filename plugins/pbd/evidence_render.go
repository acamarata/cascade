package pbd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

type evidenceOptions struct {
	ticket string
	watch  bool
	json   bool
}

func parseEvidenceArgs(args []string) (evidenceOptions, error) {
	var opts evidenceOptions
	for _, arg := range args {
		switch arg {
		case "--watch":
			opts.watch = true
		case "--json":
			opts.json = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, cascade.Newf(cascade.KindInvalidInput, "pbd evidence: unknown flag %q", arg)
			}
			if opts.ticket != "" {
				return opts, cascade.New(cascade.KindInvalidInput, "pbd evidence: only one ticket argument is allowed")
			}
			opts.ticket = arg
		}
	}
	if opts.ticket == "" {
		return opts, ErrEvidenceTicketRequired
	}
	return opts, nil
}

// RunEvidence renders a ticket's evidence in sequence order, optionally
// following new entries until cancellation. Reader errors retain their identity.
func RunEvidence(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseEvidenceArgs(args)
	if err != nil {
		return err
	}
	reader := snapshotEvidenceReader()
	rows, err := queryEvidence(ctx, reader, opts.ticket)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		if err := writeEvidenceLine(stderr, "no evidence recorded for "+opts.ticket); err != nil {
			return err
		}
	}
	if err := renderEvidenceRows(stdout, rows, opts.json); err != nil {
		return err
	}
	if !opts.watch {
		return nil
	}
	err = reader.Watch(ctx, opts.ticket, func(row EvidenceEntry) error {
		return renderEvidenceRow(stdout, row, opts.json)
	})
	if ctx.Err() != nil && err == ctx.Err() {
		return nil
	}
	return err
}

func renderEvidenceRows(out io.Writer, rows []EvidenceEntry, asJSON bool) error {
	if asJSON {
		return writeEvidenceJSON(out, rows)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := writeEvidenceLine(out, "SEQ KIND OUTCOME DATA_CLASS PRODUCER ARTIFACT_REF"); err != nil {
		return err
	}
	for _, row := range rows {
		if err := renderEvidenceRow(out, row, false); err != nil {
			return err
		}
	}
	return nil
}

func renderEvidenceRow(out io.Writer, row EvidenceEntry, asJSON bool) error {
	row = renderableEvidence(row)
	if asJSON {
		return writeEvidenceJSON(out, row)
	}
	artifact := row.ArtifactRef
	if row.Redacted {
		artifact = "redacted: true"
	}
	return writeEvidenceLine(out, fmt.Sprintf("%d %s %s %s %s %s",
		row.Seq, row.Kind, row.Outcome, row.DataClass, row.ProducerCapability, artifact))
}

func writeEvidenceJSON(out io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "pbd evidence: encode rows")
	}
	return writeEvidenceLine(out, string(data))
}

func writeEvidenceLine(out io.Writer, line string) error {
	if _, err := fmt.Fprintln(out, line); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "pbd evidence: write output")
	}
	return nil
}
