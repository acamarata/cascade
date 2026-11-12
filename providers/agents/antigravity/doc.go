// Package antigravity runs the pinned agy stream-json print interface.
// Construction probes the configured executable and fails closed on interface
// drift. The host supplies custody, the exact environment and an output filter.
// Vendor TLS traffic is outside the driver's stdio filtering boundary.
package antigravity
