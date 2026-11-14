# Node RPC methods

Methods over enrolled nodes. Access classes and elevation are listed in the
generated RPC method reference. For the node model see
[nodes](../nodes.md).

### `node.upgrade`

Reinstalls the Cascade binary on one node or on every node. Elevated: the
call always needs an elevation grant.

Params: `node_id`, or `all: true`. One of the two is required; neither is
`invalid-input`. With `all`, drained nodes are left out. With `node_id`, an
unknown node fails with the store's `not-found` error.

Result: a JSON array with one outcome per target node, in the order handled.
Each outcome carries `node_id` and then one of:

- `installed: true` with `previous_version` and `new_version`;
- `skipped: true`, with a `reason` when the node has no configured ssh or VPN
  route, or when provisioning found nothing to do;
- `err`, the provisioning failure for that node.

`skew_warning` is true when the node's previous version would fall outside
the controller's supported version window. A failure on one node does not stop
the others: it is reported in that node's `err`.

### `node.channel.hello`

Proves the controller's identity to a node over a node-facing channel. It is
registered only on the node-facing registry, with the node access class, and
is never offered on the owner socket.

Params: `nonce`, a fresh 32-byte value chosen by the node. Any `node_id` the
caller sends is ignored: the signature is bound to the authenticated channel
identity. A missing channel identity is `permission-denied`; a malformed
params object or a nonce that is not 32 bytes is `invalid-input`.

Result: `signature`, an ed25519 signature by the installation signer over the
domain tag, the channel node id and the nonce. The node verifies it against
the controller key it pinned at enrollment. Without a pinned key the node
refuses the channel with `permission-denied`.
