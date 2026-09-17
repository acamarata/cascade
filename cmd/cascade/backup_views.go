// Purpose: the human rendering of every `cascade backup …` result.
//
// WHY THIS FILE EXISTS. output.Writer.Result calls fmt.Stringer and
// nothing else (R-14.253 Finding 2), so a result handed over without one
// prints Go's default formatting. Until the W-4 hardening gate ran the
// shipped artifact, `cascade backup list` printed `{[]}` and `cascade
// backup target list` printed `map[targets:[]]` — on the epic whose
// acceptance drill is recovering a lost laptop, which is the moment an
// operator most needs to read the output.
//
// Inputs: the result types the backup verbs already produce.
// Outputs: one String method each, and the two typed wrappers the map
//
//	literals became.
//
// Constraints: the --json document does not change. Each wrapper carries
//
//	the same field names the map had, so a script parsing --json sees the
//	same keys it saw before.
//
// SPORT: cmd/cascade/backup views (ADD) — P1-E19-W4-S42-T7.
package main

import (
	"bytes"
	"fmt"
	"text/tabwriter"

	"github.com/acamarata/cascade/internal/backup"
)

// String renders one created snapshot.
func (v backupCreateResult) String() string {
	return fmt.Sprintf("snapshot %s created: %d object(s) across %s, key from %s",
		v.Snapshot, v.ObjectCount, joinOrNone(v.Domains), v.AgeIdentitySource)
}

// String renders the snapshot list.
//
// The empty case says so in words. `{}` told an operator nothing, and
// worse, told them nothing in a way they could not distinguish from a
// broken command.
func (v backupListResult) String() string {
	if len(v.Snapshots) == 0 {
		return "no backup snapshots"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SNAPSHOT\tCREATED\tTARGET\tOBJECTS\tOUTCOME\tDOMAINS")
	for _, s := range v.Snapshots {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			s.ID, s.Created.UTC().Format("2006-01-02 15:04"), s.Target,
			s.ObjectCount, s.Outcome, joinOrNone(s.Domains))
	}
	_ = tw.Flush()
	return buf.String()
}

// backupTargetListResult is what `backup target list` returns.
//
// A named type rather than the map literal it was: the map had no String
// method and could not be given one.
type backupTargetListResult struct {
	Targets []backupTargetView `json:"targets"`
}

// String renders the configured targets.
func (v backupTargetListResult) String() string {
	if len(v.Targets) == 0 {
		return "no backup targets are configured; add one with `cascade backup target add`"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tKIND\tLOCATION\tSCHEDULE\tDOMAINS")
	for _, t := range v.Targets {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			t.Name, t.Kind, t.LocationRef, orNone(t.Cron), joinOrNone(t.Domains))
	}
	_ = tw.Flush()
	return buf.String()
}

// String renders one target.
func (v backupTargetView) String() string {
	return fmt.Sprintf("%s (%s) at %s, schedule %s, domains %s",
		v.Name, v.Kind, v.LocationRef, orNone(v.Cron), joinOrNone(v.Domains))
}

// backupTargetRemovedResult reports one removal.
type backupTargetRemovedResult struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

// String says what went.
func (v backupTargetRemovedResult) String() string {
	return fmt.Sprintf("backup target %s removed", v.Name)
}

// backupExportResult reports a portable export.
type backupExportResult struct {
	Snapshot      backup.SnapshotID `json:"snapshot"`
	Target        string            `json:"target"`
	Output        string            `json:"output"`
	VaultIncluded bool              `json:"vault_included"`
}

// String says what was written and whether the vault rode along — the
// second being the fact that decides where the file may be stored.
func (v backupExportResult) String() string {
	vault := "without the vault"
	if v.VaultIncluded {
		vault = "WITH the vault (treat this file as secret material)"
	}
	return fmt.Sprintf("snapshot %s from target %s exported to %s, %s", v.Snapshot, v.Target, v.Output, vault)
}

// backupKeyExportResult reports an escrowed recovery key.
type backupKeyExportResult struct {
	Output                string `json:"output"`
	ManifestSigningPubKey string `json:"manifest_signing_pubkey"`
	Guidance              string `json:"guidance"`
}

// String puts the guidance where a human reads it. The guidance is the
// whole point of this verb: a recovery key stored beside the backups it
// decrypts protects nobody.
func (v backupKeyExportResult) String() string {
	return fmt.Sprintf("recovery key written to %s\nmanifest signing pubkey: %s\n\n%s",
		v.Output, v.ManifestSigningPubKey, v.Guidance)
}

// backupKeyImportResult reports a loaded recovery key.
type backupKeyImportResult struct {
	Input  string `json:"input"`
	Loaded bool   `json:"loaded"`
}

// String says what was loaded.
func (v backupKeyImportResult) String() string {
	if !v.Loaded {
		return "recovery key from " + v.Input + " was NOT loaded"
	}
	return "recovery key loaded from " + v.Input
}

// orNone renders a string, naming the empty case.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
