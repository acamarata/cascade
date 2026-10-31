# Process-plugin host calls

Objective: `contract:plugin-host-calls` serves plugin requests through the
capability boundary and returns a response carrying the original JSON-RPC ID.
Plugin requests and host call responses use separate transport routes, including
when their IDs collide. Each transport has one consumer through its lifetime.

`host_http_request` checks declared `net.http:<scope>` grants, then substitutes
vault values through `plugin-host-http` before hashing or sending the request.
An intent precedes RoundTrip. A completed request is confirmed; a transport
failure is unknown-outcome. Recovery uses the trusted launch recorder lock
protocol and never resends an orphaned HTTP effect. Restricted and local-only
content are refused by this class. Redirects are returned without following.

Storage operations accept only the literal `plugin:<id>` domain. Path traversal
keys are refused. Secret references accept only vault names prefixed with
`plugin.<id>.` and return a plugin-scoped reference identifier, never a value.
Event and stream emission use the policy engine and the `plugin.<id>` bus
namespace. Log content is substituted and tagged with the plugin ID.

Missing wiring returns unavailable, denied operations return capability-denied,
and unknown methods return unsupported. A notification-shaped host call never
executes. Its audit row records the method and sorted parameter keys without
parameter values. Process plugins retain the trusted-tier direct-network risk;
this contract covers host-served operations.

Acceptance tests exercise the real executable installed by signed `plugin.add`,
network denial and substitution, reference and storage scoping, construction
failure, restart consumer count, notification privacy and crash recovery.
