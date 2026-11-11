package client

// Purpose: typed inbox calls over the shared Client.Do path.
// Inputs: list filters, an item ID and a body continuation offset.
// Outputs: the notification service's wire results or a taxonomy error.
// Constraints: caller identity determines scope; parameters never declare it.

import (
	"context"

	"github.com/acamarata/cascade/internal/notify"
)

// InboxListParams filters the resolved caller's inbox without declaring scope.
type InboxListParams = notify.InboxListParams

// InboxList calls inbox.list and returns envelopes and unread counts.
func (c *Client) InboxList(ctx context.Context, params InboxListParams) (notify.InboxListResult, error) {
	var res notify.InboxListResult
	if err := c.Do(ctx, "inbox.list", params, &res); err != nil {
		return notify.InboxListResult{}, err
	}
	return res, nil
}

// InboxRead calls inbox.read for a bounded body page at the byte offset.
// Use the returned NextOffset to continue reading the item's body.
func (c *Client) InboxRead(ctx context.Context, id string, offset int) (notify.InboxReadResult, error) {
	var res notify.InboxReadResult
	if err := c.Do(ctx, "inbox.read", notify.InboxReadParams{ID: id, Offset: offset}, &res); err != nil {
		return notify.InboxReadResult{}, err
	}
	return res, nil
}

// InboxAck calls inbox.ack to acknowledge an item for the resolved caller.
func (c *Client) InboxAck(ctx context.Context, id string) (notify.InboxAckResult, error) {
	var res notify.InboxAckResult
	if err := c.Do(ctx, "inbox.ack", notify.InboxAckParams{ID: id}, &res); err != nil {
		return notify.InboxAckResult{}, err
	}
	return res, nil
}
