# Gateway wire-contract fixtures for intake tests

Source finding: `providers/anthropic/testdata/gateway/README.md`
(P1-GWY-01 contract:gateway-wire-fixtures). That finding states no live capture
exists. These are constructed fixtures, not copies of captured responses.
They follow the public Messages API and models-list JSON envelopes and use
only synthetic values. They test local registration behavior, not live
gateway compatibility.

## Files

- models.json: GET /v1/models response with synthetic model ids (model-a, model-b).
- messages_ok.json: non-streaming 1-token completion success body.
- unauthorized_401.json: 401 error body returned for an invalid bearer token.

All hostnames are scrubbed to gateway.invalid; model ids are synthetic; no
real account identifier, keychain item or helper name appears.
