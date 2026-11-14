# Personal assistant bridge RPC methods

Methods for the personal assistant bridge. Access classes are listed in the
generated RPC method reference.

### `pa.pair_code`

Issues a pairing code for the bridge. The method is registered when a bridge
module supplies a code issuer, even if the bridge itself is disabled or failed
to start.

Params: optional `subject`. An empty subject asks for the bridge's own configured subject;
the body may be empty. An unreadable body is `invalid-input`.

Result: `{"code": "...", "subject": "...", "expires_at": "..."}`, with
`expires_at` in RFC 3339. Any error from the issuer is returned unchanged.
