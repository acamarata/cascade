// Purpose: `cascade memory` (07-CLI-COMMAND-TREE §memory: "remember ·
//
//	recall · forget · list"; rpc: memory.*) — the first surface through
//	which a user can write, read and DESTROY their own memory records.
//	This file is the CLI half: the cobra tree, the injected call seam that
//	reaches the daemon through the Go IPC client SDK (internal/client,
//	never a hand-rolled JSON-RPC request — .golangci.yml's
//	cmd-rpc-server-boundary depguard rule), and the flag surface. Rendering
//	and the diagnostic scrub live in memory_view.go.
//
// Inputs: cobra args/flags; a memoryDeps injected at construction so no
//
//	test touches the real environment or a real socket (Art.7.1).
//
// Outputs: process output via internal/output only; a typed taxonomy error
//
//	on failure, with its message scrubbed of machine paths and
//	secret-shaped values (memory_view.go's scrubDiagnostic).
//
// Constraints: every verb is non-interactive — nothing here prompts, so
//
//	CASCADE_NO_INPUT has no effect (§5.8). `forget` destroys user data
//	with no prompt available to it, so it carries an explicit --dry-run
//	rehearsal and reports exactly what it removed rather than exiting
//	silently. No platform-specific imports (Art.5).
//
// SPORT: cmd.cascade.cmd.memory (ADD, per T-3 sport_updates).
package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memoryDialTimeout bounds the whole dial+request+response round trip
// against the daemon's local unix socket. A fixed duration literal, not a
// clock read (Art.7.3 governs bare time.Now/After/Tick/Sleep).
const memoryDialTimeout = 5 * time.Second

// memoryDefaultK is `memory recall`'s default result cap: a recall with
// no bound would page a whole store into a terminal.
const memoryDefaultK = 10

// memoryCallFunc performs one JSON-RPC call against the daemon at
// socketPath, decoding the result into out.
//
// It is the injected seam this file's tests substitute. That matters for
// more than convenience: a test that supplied a net.Conn instead would
// have to import "net", which the no-network unit-lane gate (Art.7.2)
// forbids in an untagged _test.go file, so the whole command would only
// ever be testable in the integration lane. The real transport is proven
// against a real daemon socket in memory_integration_test.go.
type memoryCallFunc func(ctx context.Context, socketPath, method string, params, out any) error

// memoryDeps carries every external input `cascade memory` needs.
type memoryDeps struct {
	Paths   runtime.PathProvider
	Getenv  runtime.Getenv
	Environ func() []string
	// Call reaches the daemon. Production uses the SDK client; tests
	// substitute a recorder.
	Call memoryCallFunc
	// Editor launches $EDITOR for `memory soul edit`. It is injected for
	// the reason Call is: a test must be able to prove the
	// CASCADE_NO_INPUT guard fires BEFORE a subprocess is created, which
	// is only observable when creating one is something a test can watch.
	Editor soulEditorFunc
}

// productionMemoryDeps builds memoryDeps against the real environment.
func productionMemoryDeps() memoryDeps {
	return memoryDeps{
		Paths:   lazyPaths{},
		Getenv:  os.Getenv,
		Environ: os.Environ,
		Call:    clientMemoryCall,
		Editor:  productionSoulEditor,
	}
}

// clientMemoryCall is the production memoryCallFunc: one SDK client per
// invocation, dialing the real unix socket. The whole JSON-RPC framing
// lives in internal/client; nothing is assembled here.
func clientMemoryCall(ctx context.Context, socketPath, method string, params, out any) error {
	c := client.New(socketPath, client.UnixDialer, memoryDialTimeout)
	return c.Do(ctx, method, params, out)
}

// memoryStoreDir is where the memory files live: {CASCADE_HOME}/memory.
//
// It sits beside the config file rather than under data/, deliberately.
// The store's whole premise is that the markdown files ARE the record and
// a user may edit them with any editor; burying them in a machine-managed
// data directory would make the one thing users are expected to open the
// hardest thing to find.
func memoryStoreDir(paths runtime.PathProvider) string {
	return filepath.Join(paths.Root(), "memory")
}

// mountMemoryCmd attaches the top-level `memory` command tree, following
// mountStatusCmd's exact pattern.
func mountMemoryCmd(root *cobra.Command) {
	cmd := newMemoryCmd(productionMemoryDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newMemoryCmd builds the `memory` command and its four verbs.
func newMemoryCmd(deps memoryDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Remember, recall and forget memory records",
		Long: "Read and write the memory store: the markdown files that are the\n" +
			"source of truth for everything cascade remembers.\n\n" +
			"Every verb but `soul edit` is non-interactive and takes its input\n" +
			"from flags and arguments only; `soul edit` opens $EDITOR and has\n" +
			"a --content <file> equivalent for automation. `memory forget` and\n" +
			"`memory consolidate` retire records; run either with --dry-run\n" +
			"first to see exactly what it would retire.",
	}
	cmd.AddCommand(
		newMemoryRememberCmd(deps),
		newMemoryRecallCmd(deps),
		newMemoryForgetCmd(deps),
		newMemoryListCmd(deps),
		newMemorySoulCmd(deps),
		newMemoryConsolidateCmd(deps),
	)
	return cmd
}

// newMemoryRememberCmd builds `cascade memory remember`.
func newMemoryRememberCmd(deps memoryDeps) *cobra.Command {
	var params memory.RememberParams
	cmd := &cobra.Command{
		Use:   "remember <content>",
		Short: "Write a memory record and print its canonical address",
		Long: "Write <content> as a memory record and print its canonical\n" +
			"\"<kind>/<name>\" address. With no --name, the name is the first 16\n" +
			"hex characters of the body's BLAKE3 hash, so remembering the same\n" +
			"body twice addresses the same record instead of duplicating it.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			params.Content = args[0]
			var result memory.RememberResult
			if err := memoryCall(cmd, deps, memory.MethodRemember, params, &result); err != nil {
				return err
			}
			return memoryWriter(cmd).Result(rememberView{result})
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&params.Type, "type", "", "memory kind: user|feedback|project|reference (default project)")
	flags.StringVar(&params.Name, "name", "", "record name within its kind (default: the body hash prefix)")
	flags.StringVar(&params.Provenance, "provenance", "", "reference for the session that produced this record")
	return cmd
}

// newMemoryRecallCmd builds `cascade memory recall`.
func newMemoryRecallCmd(deps memoryDeps) *cobra.Command {
	var params memory.RecallParams
	cmd := &cobra.Command{
		Use:   "recall <query>",
		Short: "Find memory records whose text contains a query",
		Long: "Scan the memory files for records whose name, description or body\n" +
			"contains <query>, case-insensitively, and print them in canonical\n" +
			"address order. This reads the files directly; the indexed,\n" +
			"ranked recall surface is a separate command.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			params.Query = args[0]
			var result memory.RecallResult
			if err := memoryCall(cmd, deps, memory.MethodRecall, params, &result); err != nil {
				return err
			}
			return memoryWriter(cmd).Result(unitsView{Units: result.Units, Unreadable: result.Unreadable})
		},
	}
	flags := cmd.Flags()
	flags.IntVar(&params.K, "k", memoryDefaultK, "maximum number of records to return")
	flags.StringVar(&params.Type, "type", "", "restrict the scan to one memory kind")
	return cmd
}

// newMemoryForgetCmd builds `cascade memory forget`.
func newMemoryForgetCmd(deps memoryDeps) *cobra.Command {
	var params memory.ForgetParams
	cmd := &cobra.Command{
		Use:   "forget <kind>/<name>",
		Short: "Retire one memory record by its canonical address",
		Long: "Tombstone the record at <kind>/<name>. This is destructive: the\n" +
			"record's file is removed and a tombstone is left in its place, so\n" +
			"the deletion survives an interruption. Nothing else is touched —\n" +
			"no other record, and no other kind. The bytes are NOT scrubbed\n" +
			"from the disk; that is the forget pipeline's job, not this verb's.\n\n" +
			"Nothing prompts. Use --dry-run to see what would be retired.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			params.ID = args[0]
			var result memory.ForgetResult
			if err := memoryCall(cmd, deps, memory.MethodForget, params, &result); err != nil {
				return err
			}
			return memoryWriter(cmd).Result(forgetView{result})
		},
	}
	cmd.Flags().BoolVar(&params.DryRun, "dry-run", false, "report what would be retired without retiring it")
	return cmd
}

// newMemoryListCmd builds `cascade memory list`.
func newMemoryListCmd(deps memoryDeps) *cobra.Command {
	var params memory.ListParams
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored memory records in canonical address order",
		Long: "Page through the stored records in lexicographic \"<kind>/<name>\"\n" +
			"order. Pass the printed next cursor back with --cursor to fetch\n" +
			"the following page.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var result memory.ListResult
			if err := memoryCall(cmd, deps, memory.MethodList, params, &result); err != nil {
				return err
			}
			return memoryWriter(cmd).Result(unitsView{
				Units:      result.Units,
				Unreadable: result.Unreadable,
				NextCursor: result.NextCursor,
			})
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&params.Type, "type", "", "restrict the listing to one memory kind")
	flags.IntVar(&params.Limit, "limit", 0, "maximum records per page (default 100)")
	flags.StringVar(&params.Cursor, "cursor", "", "resume after this canonical address")
	return cmd
}

// memoryCall resolves the daemon socket and performs one RPC, scrubbing
// every error on the way out. Scrubbing happens HERE, at the boundary,
// rather than at each call site, so no verb can forget to do it.
func memoryCall(cmd *cobra.Command, deps memoryDeps, method string, params, out any) error {
	return scrubDiagnostic(memoryCallRaw(cmd, deps, method, params, out))
}

// memoryCallRaw performs the same call and returns the error UNSCRUBBED,
// for one caller: `memory soul edit`, which must ask whether the failure
// was "no soul document yet" before starting the user from an empty
// document. scrubDiagnostic deliberately terminates the error chain, so
// that classification has to happen before the scrub; every error read
// through this function is scrubbed by its caller before it can reach a
// terminal.
func memoryCallRaw(cmd *cobra.Command, deps memoryDeps, method string, params, out any) error {
	socketPath, err := memoryResolveSocket(cmd.Context(), deps)
	if err != nil {
		return err
	}
	return deps.Call(cmd.Context(), socketPath, method, params, out)
}

// memoryResolveSocket loads config.toml — the single resolution model every
// CLI command uses (08 §2) — and resolves the daemon's socket path from it,
// falling back to the path provider's derived default.
func memoryResolveSocket(ctx context.Context, deps memoryDeps) (string, error) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "cascade memory: load config.toml")
	}
	settings, err := daemon.ResolveSettings(cfg, deps.Paths)
	if err != nil {
		return "", err
	}
	return settings.SocketPath, nil
}

// memoryWriter builds this command's output.Writer from the resolved global
// flags, mirroring statusOutputWriter's established local convention.
func memoryWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}
