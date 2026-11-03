package egress

// EgressClassFleetDiscovery is the fleet-discovery outbound path
// (P1-TOP-18): the registered providers' hosts transit this class during
// fleet discovery. It is audit-only until its caller is wired.
const EgressClassFleetDiscovery EgressClass = "fleet-discovery"
