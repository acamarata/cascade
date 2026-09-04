// Purpose: the operator's view of, and way out of, the secret detector's
//
//	quarantine - `cascade vault quarantine list`, `... release ID`, and
//	the `--from-quarantine` promotion path on `vault set`.
//
// Inputs: cobra args/flags plus the shared vaultDeps, which now carries a
//
//	Quarantine provider so a test points at a temp dir and never at the
//	real profile data directory.
//
// Outputs: entry METADATA through internal/output.Writer - class,
//
//	location, confidence, suggested name, source reference. A quarantine
//	entry has no value field to print, by construction, so `list` cannot
//	become a secret-disclosure surface the way a naive one would.
//
// Constraints: a promoted value is read from stdin or --value-file, never
//
//	from argv. Under CASCADE_NO_INPUT=1 with no piped stdin, promotion is
//	a hard error rather than a prompt nobody can answer. Every exit from
//	quarantine records a reason, so an entry is never simply gone.
//
// SPORT: cmd/cascade/vault (ADD - quarantine list/release, set
//
//	--from-quarantine).

package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// quarantineDirName is the profile-data subdirectory the ledger lives in.
const quarantineDirName = "quarantine"

// quarantineStore builds the store for one command invocation.
func quarantineStore(deps vaultDeps) (*secrets.QuarantineStore, error) {
	if deps.Quarantine == nil {
		return nil, cascade.New(cascade.KindInternal, "vault: no quarantine store is configured")
	}
	return deps.Quarantine()
}

// entryView is one rendered quarantine entry. There is no value field
// because QuarantineEntry has none.
type entryView struct {
	ID            string  `json:"id"`
	Class         string  `json:"class"`
	Pattern       string  `json:"pattern"`
	Offset        int     `json:"offset"`
	Length        int     `json:"length"`
	Confidence    float64 `json:"confidence"`
	SuggestedName string  `json:"suggested_name"`
	SourceRef     string  `json:"source_ref"`
	DetectedAt    string  `json:"detected_at"`
}

// quarantineListView is `vault quarantine list`'s result.
type quarantineListView struct {
	Entries []entryView `json:"entries"`
	Pending int         `json:"pending"`
}

// releaseView is `vault quarantine release`'s result.
type releaseView struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// promoteView is `vault set --from-quarantine`'s result: the name the
// value landed under, and the entry that was retired.
type promoteView struct {
	Name         string `json:"name"`
	Replaced     bool   `json:"replaced"`
	Backend      string `json:"backend"`
	QuarantineID string `json:"quarantine_id"`
}

// newVaultQuarantineCmd builds the `vault quarantine` noun.
func newVaultQuarantineCmd(deps vaultDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "Review and release what the secret detector flagged",
		Long: "Inspect the local secret-detector quarantine.\n\n" +
			"The detector records WHERE credential material was found and what shape\n" +
			"it had. It never records the value, so nothing here can print one. An\n" +
			"entry leaves quarantine in one of two ways: `cascade vault set\n" +
			"--from-quarantine ID` stores it in the vault, or `cascade vault\n" +
			"quarantine release ID` retires it as a false positive.",
		Example: "  cascade vault quarantine list\n" +
			"  cascade vault quarantine release 4f3a2b1c9d8e7f60",
		Annotations: map[string]string{"local": "true"},
	}
	cmd.AddCommand(newVaultQuarantineListCmd(deps), newVaultQuarantineReleaseCmd(deps))
	return cmd
}

// newVaultQuarantineListCmd builds `vault quarantine list`.
func newVaultQuarantineListCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "list",
		Short:       "List quarantined detections (metadata only, never values)",
		Example:     "  cascade vault quarantine list\n  cascade vault quarantine list --json",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := quarantineStore(deps)
			if err != nil {
				return err
			}
			entries, err := store.List()
			if err != nil {
				return err
			}
			return vaultOutputWriter(cmd).Result(quarantineListView{
				Entries: renderEntries(entries), Pending: len(entries),
			})
		},
	}
}

// renderEntries converts stored entries to their rendered form.
func renderEntries(entries []secrets.QuarantineEntry) []entryView {
	out := make([]entryView, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entryView{
			ID: entry.ID, Class: string(entry.Class), Pattern: entry.Pattern,
			Offset: entry.Offset, Length: entry.Length,
			Confidence: float64(entry.Confidence), SuggestedName: entry.SuggestedName,
			SourceRef: entry.SourceRef, DetectedAt: entry.DetectedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out
}

// newVaultQuarantineReleaseCmd builds `vault quarantine release`, the
// false-positive recovery path. Without it a wrong detection would be a
// permanent, unexplained entry, which is how a detector earns being
// switched off.
func newVaultQuarantineReleaseCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "release ID",
		Short: "Retire a quarantine entry the detector got wrong",
		Long: "Release a quarantined detection as a false positive.\n\n" +
			"The entry stops appearing in `quarantine list`; the ledger keeps the\n" +
			"record that it existed and that it was released, so a release is\n" +
			"accounted rather than erased.",
		Example:     "  cascade vault quarantine release 4f3a2b1c9d8e7f60",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := quarantineStore(deps)
			if err != nil {
				return err
			}
			if err := store.Delete(args[0], secrets.ReleaseFalsePositive); err != nil {
				return err
			}
			return vaultOutputWriter(cmd).Result(releaseView{
				ID: args[0], Reason: secrets.ReleaseFalsePositive,
			})
		},
	}
}

// runVaultPromote implements `vault set --from-quarantine ID`.
//
// The value itself is re-supplied by the operator: the detector recorded
// that a credential was at a place, never what it said, so there is
// nothing to recover from the ledger but the name to store it under.
func runVaultPromote(cmd *cobra.Command, deps vaultDeps, id, valueFile string) error {
	store, err := quarantineStore(deps)
	if err != nil {
		return err
	}
	entry, err := store.Get(id)
	if err != nil {
		return err
	}
	if valueFile == "" && noInput(deps) && !stdinIsPiped(deps) {
		return cascade.Newf(cascade.KindInvalidInput,
			"vault: CASCADE_NO_INPUT=1 and stdin is not piped, so the value for %s cannot be read; "+
				"pipe it in or pass --value-file", entry.SuggestedName)
	}
	broker, err := vaultBroker(deps)
	if err != nil {
		return err
	}
	value, err := readSecretValue(cmd, deps, valueFile)
	if err != nil {
		return err
	}
	result, err := broker.Set(cmd.Context(), entry.SuggestedName, value, secrets.SetUpdate)
	if err != nil {
		return err
	}
	if err := store.Delete(id, secrets.ReleasePromoted); err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(promoteView{
		Name: result.Name, Replaced: result.Replaced, Backend: broker.Backend(), QuarantineID: id,
	})
}

// stdinIsPiped reports whether stdin carries data rather than a terminal.
// It is a dependency rather than a direct os.Stdin stat so a test can
// exercise the CASCADE_NO_INPUT refusal without needing a real terminal.
func stdinIsPiped(deps vaultDeps) bool {
	if deps.StdinIsPiped == nil {
		return true
	}
	return deps.StdinIsPiped()
}

// productionStdinIsPiped is the real check: stdin is piped when it is not
// a character device.
func productionStdinIsPiped() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}
