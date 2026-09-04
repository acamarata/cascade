// Purpose: `cascade memory soul` (07-CLI-COMMAND-TREE §memory: "soul
//
//	show|edit|export (versioned, audited)") — the CLI half of the SOUL
//	store. It is a separate file from memory.go for the 300-line cap and
//	because the two guard different things: memory.go moves records,
//	this moves the system's model of the user, whose export is the single
//	most dangerous operation the command tree offers.
//
// Inputs: cobra args/flags; the memoryDeps seam (Getenv for
//
//	CASCADE_NO_INPUT and $EDITOR, Editor for the subprocess), so no test
//	touches a real editor or a real socket.
//
// Outputs: process output via internal/output only; taxonomy errors
//
//	scrubbed of machine paths and secret-shaped values on the way out.
//
// Constraints: `soul edit` is the only interactive verb in the memory
//
//	tree, so it carries the §5 rule 8 pair — a --content <file>
//	non-interactive equivalent, and a CASCADE_NO_INPUT=1 hard error
//	raised BEFORE any subprocess is created. `soul export` writes the
//	export envelope and nothing else.
//
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).
package main

import (
	"context"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// soulEditorFunc launches the user's editor against path. It is the seam
// this file's tests substitute: a unit test must be able to prove that the
// CASCADE_NO_INPUT guard fires BEFORE a subprocess exists, which is only
// observable if creating that subprocess is something the test can watch.
type soulEditorFunc func(ctx context.Context, cmd *cobra.Command, editor, path string) error

// soulScratchPattern names the temporary file $EDITOR opens.
const soulScratchPattern = "cascade-soul-edit-*.md"

// newMemorySoulCmd builds `cascade memory soul` and its three verbs.
func newMemorySoulCmd(deps memoryDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "soul",
		Short: "Show, edit and export the persistent identity document",
		Long: "The SOUL is the durable document describing the person cascade\n" +
			"serves. Every change to it is versioned and audited, whichever\n" +
			"route it arrives by: this command, an edit made directly to the\n" +
			"file, or the chat surface.\n\n" +
			"`soul export` writes the document and its whole change log to one\n" +
			"JSON file. That file is the most personal thing cascade produces —\n" +
			"treat it the way you would treat a copy of your own notes.",
	}
	cmd.AddCommand(
		newMemorySoulShowCmd(deps),
		newMemorySoulEditCmd(deps),
		newMemorySoulExportCmd(deps),
	)
	return cmd
}

// newMemorySoulShowCmd builds `cascade memory soul show`.
func newMemorySoulShowCmd(deps memoryDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the current soul document and its version",
		Long: "Print the document and the version it is at. Reading also\n" +
			"reconciles an edit made directly to the file: an out-of-store\n" +
			"edit is adopted here, versioned and recorded, rather than\n" +
			"silently overwritten by the next write.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result memory.SoulShowResult
			if err := memoryCall(cmd, deps, memory.MethodSoulShow,
				memory.SoulShowParams{}, &result); err != nil {
				return err
			}
			return memoryWriter(cmd).Result(soulShowView{result})
		},
	}
}

// newMemorySoulEditCmd builds `cascade memory soul edit`.
func newMemorySoulEditCmd(deps memoryDeps) *cobra.Command {
	var contentPath string
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit the soul document in $EDITOR, or apply a file",
		Long: "Open the current document in $EDITOR and apply what you save.\n" +
			"The change is versioned and recorded like any other.\n\n" +
			"For automation, --content <file> applies a file's contents with\n" +
			"no editor at all. With CASCADE_NO_INPUT=1 set, opening an editor\n" +
			"is a hard error rather than a hang: use --content there.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSoulEdit(cmd, deps, contentPath)
		},
	}
	cmd.Flags().StringVar(&contentPath, "content", "",
		"apply this file's contents instead of opening an editor")
	return cmd
}

// newMemorySoulExportCmd builds `cascade memory soul export`.
func newMemorySoulExportCmd(deps memoryDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Write the soul document and its full audit log as JSON",
		Long: "Print the export envelope: the schema version, the instant of\n" +
			"export, the document, and every recorded change in version order.\n" +
			"The change log names versions, routes, instants and digests — it\n" +
			"never carries the document text of any past version.\n\n" +
			"Nothing else is included: no other memory record, no file path,\n" +
			"and nothing about this machine.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result memory.SoulExport
			if err := memoryCall(cmd, deps, memory.MethodSoulExport,
				memory.SoulExportParams{}, &result); err != nil {
				return err
			}
			return memoryWriter(cmd).Result(soulExportView{result})
		},
	}
}

// runSoulEdit applies either a file (--content) or an editor session.
func runSoulEdit(cmd *cobra.Command, deps memoryDeps, contentPath string) error {
	body, err := soulEditBody(cmd, deps, contentPath)
	if err != nil {
		return err
	}
	var result memory.SoulEditResult
	if err := memoryCall(cmd, deps, memory.MethodSoulEdit,
		memory.SoulEditParams{Body: body}, &result); err != nil {
		return err
	}
	return memoryWriter(cmd).Result(soulEditView{result})
}

// soulEditBody produces the new document text.
//
// The --content path never consults the editor, the environment or a
// terminal, which is what makes it the automation equivalent the §5 rule 8
// parity requires rather than a convenience flag.
func soulEditBody(cmd *cobra.Command, deps memoryDeps, contentPath string) (string, error) {
	if contentPath != "" {
		data, err := os.ReadFile(contentPath) //nolint:gosec // the path is the operator's own argument
		if err != nil {
			return "", scrubDiagnostic(cascade.Wrap(cascade.KindInvalidInput, err,
				"cascade memory soul edit: read --content file"))
		}
		return string(data), nil
	}
	return soulEditViaEditor(cmd, deps)
}

// soulEditViaEditor runs the interactive path.
//
// The CASCADE_NO_INPUT guard is the FIRST thing it does. That ordering is
// the whole point: an automation environment that set the variable must
// get a refusal it can read, not an editor subprocess waiting forever on a
// terminal that is not there.
func soulEditViaEditor(cmd *cobra.Command, deps memoryDeps) (string, error) {
	if soulNoInput(deps) {
		return "", scrubDiagnostic(cascade.Wrapf(cascade.KindInvalidInput,
			memory.ErrSoulEditNeedsInput,
			"cascade memory soul edit: %s", memory.SoulNoInputMessage))
	}
	current, err := soulCurrentBody(cmd, deps)
	if err != nil {
		return "", err
	}
	path, cleanup, err := writeSoulScratchFile(current)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if err := deps.Editor(cmd.Context(), cmd, soulEditorCommand(deps), path); err != nil {
		return "", scrubDiagnostic(err)
	}
	edited, err := os.ReadFile(path) //nolint:gosec // path is this process's own scratch file
	if err != nil {
		return "", scrubDiagnostic(cascade.Wrap(cascade.KindInternal, err,
			"cascade memory soul edit: read edited document"))
	}
	return string(edited), nil
}

// soulNoInput reports whether CASCADE_NO_INPUT=1 forbids opening an
// editor, matching vault.go's own reading of the same variable.
func soulNoInput(deps memoryDeps) bool {
	return deps.Getenv != nil && deps.Getenv("CASCADE_NO_INPUT") == "1"
}

// soulEditorCommand resolves $EDITOR, falling back to vi — the same POSIX
// convention `cascade config edit` already uses.
func soulEditorCommand(deps memoryDeps) string {
	if deps.Getenv != nil {
		if e := deps.Getenv("EDITOR"); e != "" {
			return e
		}
	}
	return "vi"
}

// soulCurrentBody fetches the document to edit, treating "none yet" as an
// empty starting point rather than a failure: the first `soul edit` is how
// a user writes their SOUL for the first time.
func soulCurrentBody(cmd *cobra.Command, deps memoryDeps) (string, error) {
	var result memory.SoulShowResult
	err := memoryCallRaw(cmd, deps, memory.MethodSoulShow, memory.SoulShowParams{}, &result)
	if err == nil {
		return result.Body, nil
	}
	if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindNotFound {
		return "", nil
	}
	return "", scrubDiagnostic(err)
}

// writeSoulScratchFile copies body into a fresh scratch file for the
// editor to open, returning its path and a cleanup that removes it.
//
// The scratch file holds the user's identity document, so it is created
// with owner-only permissions by os.CreateTemp and removed on every exit
// path, including a failed edit.
func writeSoulScratchFile(body string) (path string, cleanup func(), err error) {
	tmp, err := os.CreateTemp("", soulScratchPattern)
	if err != nil {
		return "", nil, scrubDiagnostic(cascade.Wrap(cascade.KindInternal, err,
			"cascade memory soul edit: create scratch file"))
	}
	cleanup = func() { _ = os.Remove(tmp.Name()) }
	if _, werr := tmp.WriteString(body); werr != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, scrubDiagnostic(cascade.Wrap(cascade.KindInternal, werr,
			"cascade memory soul edit: write scratch file"))
	}
	if cerr := tmp.Close(); cerr != nil {
		cleanup()
		return "", nil, scrubDiagnostic(cascade.Wrap(cascade.KindInternal, cerr,
			"cascade memory soul edit: close scratch file"))
	}
	return tmp.Name(), cleanup, nil
}

// productionSoulEditor is the real soulEditorFunc: it launches the
// resolved editor wired to the command's own stdio, so an interactive
// editor behaves exactly as it would from a terminal.
func productionSoulEditor(ctx context.Context, cmd *cobra.Command, editor, path string) error {
	//nolint:gosec // $EDITOR is an intentional, user-controlled launch, the standard `git commit` pattern
	editorCmd := exec.CommandContext(ctx, editor, path)
	editorCmd.Stdin = cmd.InOrStdin()
	editorCmd.Stdout = cmd.OutOrStdout()
	editorCmd.Stderr = cmd.OutOrStderr()
	if err := editorCmd.Run(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "cascade memory soul edit: run $EDITOR")
	}
	return nil
}
