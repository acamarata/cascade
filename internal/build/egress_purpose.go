package build

// PurposeLocalTransport names control sockets that stay on this machine.
const PurposeLocalTransport = "local-transport"

// PurposeTypeOnly names imports used only for network types.
const PurposeTypeOnly = "type-only"

// PurposeProcessTierDeclared names a process plugin's declared network scope.
const PurposeProcessTierDeclared = "process-tier-declared"

// EgressNetPurpose assigns each admitted package a class or a non-egress purpose.
var EgressNetPurpose = map[string]string{
	"internal/secrets":                      "oauth",
	"internal/providers/intake":             "provider-intake",
	"providers/embeddings/bgem3":            "provider-intake",
	"providers/openai":                      "provider-intake",
	"providers/redis":                       "provider-intake",
	"providers/s3":                          "provider-intake",
	"providers/transport":                   "provider-intake",
	"internal/plugins/remote":               "plugin-remote",
	"internal/plugins/registryfetch":        "registry-fetch",
	"internal/nodes":                        "node-dispatch",
	"plugins/cascade-pa/telegram":           "bridge",
	"plugins/github":                        "ci-poll",
	"plugins/github/tools":                  PurposeTypeOnly,
	"plugins/pbd":                           PurposeProcessTierDeclared,
	"internal/providers/agents/egressproxy": PurposeProcessTierDeclared,
	"internal/update":                       "release-download",
	"cmd/cascade":                           PurposeLocalTransport,
	"internal/runtime":                      PurposeLocalTransport,
	"internal/client":                       PurposeLocalTransport,
	"internal/daemon":                       PurposeLocalTransport,
	"internal/rpc":                          PurposeLocalTransport,
	"internal/fleet/sessions":               PurposeLocalTransport,
	"internal/providers/agents":             PurposeLocalTransport,
	"internal/elevation":                    PurposeLocalTransport,
	"cmd/cascade-elevate-helper":            PurposeLocalTransport,
	// The gateway owner registers this class when its importer lands.
	"internal/providers/gateway": "provider-usage",
}

// EgressNetLocalTransport separates local sockets from outbound transports.
var EgressNetLocalTransport = []EgressAllowEntry{
	{"internal/client", "Control-socket client"},
	{"internal/daemon", "Control-socket listener"},
	{"internal/rpc", "Control-plane RPC frames"},
	{"internal/fleet/sessions", "Session event stream over the control socket"},
}
