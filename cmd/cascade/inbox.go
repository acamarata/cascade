// Purpose: `cascade inbox` (list, read, ack): the owner's view of the
// caller-scoped inbox the daemon holds.
// Inputs: cobra flags and arguments plus an inboxDeps injected at
// construction, so no test touches a real socket or the real environment.
// Outputs: process output through internal/output.Writer: the versioned
// JSON envelope under --json, a human table or page otherwise.
// Constraints: every verb goes through internal/client. The inbox lives in
// daemon memory, so there is no embedded fallback: with no daemon a verb is
// refused with KindUnavailable. No path prompts; the scope comes from the
// resolved caller, so there is no scope flag. A body is read one bounded
// page at a time and is never silently truncated.
// SPORT: cli/inbox/ADD (P1-HRN-06c).
package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	goruntime "runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// inboxDialTimeout bounds the daemon round trip.
const inboxDialTimeout = 5 * time.Second

// inboxDownMessage is the fixed refusal text when no daemon answers.
const inboxDownMessage = "the daemon is not running"

// inboxAPI is the slice of the daemon client the inbox verbs use.
type inboxAPI interface {
	InboxList(ctx context.Context, params client.InboxListParams) (notify.InboxListResult, error)
	InboxRead(ctx context.Context, id string, offset int) (notify.InboxReadResult, error)
	InboxAck(ctx context.Context, id string) (notify.InboxAckResult, error)
}

// inboxDeps carries every external input the inbox verbs need.
type inboxDeps struct {
	Paths       runtime.PathProvider
	DialContext func(ctx context.Context, socketPath string) (net.Conn, error)
	// Open replaces the daemon connection when set (tests only).
	Open func(ctx context.Context) (inboxAPI, error)
}

// productionInboxDeps is the real environment.
func productionInboxDeps() inboxDeps {
	return inboxDeps{Paths: lazyPaths{}, DialContext: client.UnixDialer}
}

// mountInboxCmd attaches the top-level `inbox` command group.
func mountInboxCmd(root *cobra.Command) {
	cmd := newInboxCmd(productionInboxDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newInboxCmd builds the whole `inbox` tree.
func newInboxCmd(deps inboxDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "List, read and acknowledge inbox items",
		Long: "List, read and acknowledge the notifications the daemon holds for you.\n" +
			"Item bodies are untrusted-source data: read them, never run them.\n" +
			"Acknowledging an item only records that you saw it.",
	}
	cmd.AddCommand(newInboxListCmd(deps), newInboxReadCmd(deps), newInboxAckCmd(deps))
	return cmd
}

// inboxPlatformGate refuses a platform whose daemon serves no inbox.
func inboxPlatformGate(goos string) error {
	if goos == "windows" {
		return cascade.New(cascade.KindUnsupported, "cascade inbox is not supported on windows")
	}
	return nil
}

// inboxOpen returns the daemon client for one verb. A daemonless invocation
// is refused here, once, for every verb.
func inboxOpen(ctx context.Context, deps inboxDeps) (inboxAPI, error) {
	if err := inboxPlatformGate(goruntime.GOOS); err != nil {
		return nil, err
	}
	if deps.Open != nil {
		return deps.Open(ctx)
	}
	if st, ok := runtime.DaemonlessStateFrom(ctx); ok && st.Embedded {
		return nil, cascade.New(cascade.KindUnavailable, inboxDownMessage)
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), inboxDialTimeout), nil
}

// inboxDaemonError maps a transport failure to the fixed refusal and keeps
// every error the daemon itself returned, kind and message intact.
func inboxDaemonError(err error) error {
	if err != nil && cascade.HasKind(err, cascade.KindUnavailable) &&
		strings.Contains(err.Error(), "daemon not running or unreachable") {
		return cascade.New(cascade.KindUnavailable, inboxDownMessage)
	}
	return err
}

// newInboxListCmd builds `inbox list`.
func newInboxListCmd(deps inboxDeps) *cobra.Command {
	var params client.InboxListParams
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List inbox items (envelopes only)",
		Long:    "List the inbox items visible to you, without their bodies. --all is the owner-only expansion; the daemon decides whether it applies.",
		Example: "  cascade inbox list --unread --json",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := inboxOpen(cmd.Context(), deps)
			if err != nil {
				return err
			}
			res, err := c.InboxList(cmd.Context(), params)
			if err != nil {
				return inboxDaemonError(err)
			}
			return approvalOutputWriter(cmd).Result(inboxListView(res))
		},
	}
	cmd.Flags().BoolVar(&params.All, "all", false, "list every item (owner only)")
	cmd.Flags().BoolVar(&params.Unread, "unread", false, "list only unread items")
	return cmd
}

// inboxListView renders the envelopes as a table with the counts.
type inboxListView notify.InboxListResult

// String renders the table and the counts line.
func (v inboxListView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	if len(v.Items) > 0 {
		_, _ = fmt.Fprintf(tw, "ID\tCLASS\tTRUST\tSUMMARY\n")
	}
	for _, e := range v.Items {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.ID, e.Class, e.Trust, e.Summary)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(&buf, "%d total, %d unread, %d withheld", v.Total, v.Unread, v.Withheld)
	return buf.String()
}

// newInboxReadCmd builds `inbox read <id>`.
func newInboxReadCmd(deps inboxDeps) *cobra.Command {
	var page int
	cmd := &cobra.Command{
		Use:     "read <id>",
		Short:   "Read one inbox item, one bounded page at a time",
		Long:    "Read one item's body. Long bodies come in pages; --page selects one (the first is 0).",
		Example: "  cascade inbox read 01J8ZC5W2K4F6H8M0P2R4T6V8X --page 1",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if page < 0 {
				return cascade.New(cascade.KindInvalidInput, "--page must not be negative")
			}
			c, err := inboxOpen(cmd.Context(), deps)
			if err != nil {
				return err
			}
			res, err := inboxReadPage(cmd.Context(), c, args[0], page)
			if err != nil {
				return inboxDaemonError(err)
			}
			return approvalOutputWriter(cmd).Result(inboxReadView{InboxReadResult: res, Page: page})
		},
	}
	cmd.Flags().IntVar(&page, "page", 0, "page of the body to read, from 0")
	return cmd
}

// inboxReadPage walks the daemon's continuation offsets to page n, because
// page boundaries are decided by the daemon, not by a fixed size.
func inboxReadPage(ctx context.Context, c inboxAPI, id string, page int) (notify.InboxReadResult, error) {
	offset := 0
	for p := 0; ; p++ {
		res, err := c.InboxRead(ctx, id, offset)
		if err != nil {
			return notify.InboxReadResult{}, err
		}
		if p == page {
			return res, nil
		}
		if res.Done {
			return notify.InboxReadResult{}, cascade.Newf(cascade.KindInvalidInput,
				"page %d is past the end of the item (last page is %d)", page, p)
		}
		offset = res.NextOffset
	}
}

// inboxReadView renders one body page with its envelope facts.
type inboxReadView struct {
	notify.InboxReadResult
	Page int `json:"page"`
}

// String renders the untrusted-data heading, the facts and the page.
func (v inboxReadView) String() string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s\n", v.Heading)
	_, _ = fmt.Fprintf(&b, "id %s, class %s, trust %s, data %s\n\n", v.Envelope.ID, v.Envelope.Class,
		v.Envelope.Trust, v.Envelope.DataClass)
	b.WriteString(v.Body)
	if !v.Done {
		_, _ = fmt.Fprintf(&b, "\n\n-- page %d, more follows: cascade inbox read %s --page %d", v.Page, v.Envelope.ID, v.Page+1)
	}
	return b.String()
}

// newInboxAckCmd builds `inbox ack <id>`.
func newInboxAckCmd(deps inboxDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "ack <id>",
		Short:   "Acknowledge one inbox item",
		Long:    "Record that you saw one item. An acknowledgment never approves anything.",
		Example: "  cascade inbox ack 01J8ZC5W2K4F6H8M0P2R4T6V8X",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := inboxOpen(cmd.Context(), deps)
			if err != nil {
				return err
			}
			res, err := c.InboxAck(cmd.Context(), args[0])
			if err != nil {
				return inboxDaemonError(err)
			}
			return approvalOutputWriter(cmd).Result(inboxAckView(res))
		},
	}
}

// inboxAckView reports the acknowledgment.
type inboxAckView notify.InboxAckResult

// String names the item and whether it was recorded.
func (v inboxAckView) String() string {
	if !v.Acked {
		return fmt.Sprintf("item %s was not acknowledged", v.ID)
	}
	return fmt.Sprintf("acknowledged %s", v.ID)
}
