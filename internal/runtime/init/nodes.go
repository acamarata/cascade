package init

// Purpose: the opt-in LAN nodes step (P1-NODE-12d, 8b). It pairs each
//   [nodes].pair host, and each LAN host the operator accepts, with that
//   host's one-use pairing code, and records whether the node travels.
// Inputs: the merged initconfig.NodesConfig, an injected LAN discovery
//   source and the node pairer.
// Outputs: a NodesResult naming what was paired, skipped and planned.
// Constraints: windows is a typed tier-2 refusal returned before any
//   discovery or pairing call. The LAN is scanned only when scan_lan is
//   set. A code comes from the operator's prompt or from
//   CASCADE_INIT_NODE_CODE_<HOST>, never from anywhere else, and a
//   non-interactive run checks every code before the first pairing write.
//   A host the pairer already holds is never paired twice.
// SPORT: internal/runtime/init nodes step (ADD) — P1-NODE-12d.

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// windowsNodesHint is the nodes step's tier-2 refusal text on windows.
const windowsNodesHint = "cascade init: LAN discovery and node pairing are tier-2 on windows; " +
	"pair this machine from a darwin or linux controller instead"

// nodesNoCodeMsg is the refusal for a paired host with no code.
const nodesNoCodeMsg = "cascade init: no pairing code for %q; set %s or pair interactively"

// LANSource discovers candidate hosts on the local network. The wizard
// never scans itself; production wires the nodes discovery producer and
// tests inject a fixed list.
type LANSource interface {
	// Scan returns the hosts answering on the LAN.
	Scan(ctx context.Context) ([]string, error)
}

// NodePairer pairs this machine with a host through the nodes package.
type NodePairer interface {
	// Paired lists the hosts already paired, so a replay skips them.
	Paired(ctx context.Context) ([]string, error)
	// Pair consumes host's one-use code; travel is recorded on the node.
	Pair(ctx context.Context, host, code string, travel bool) error
}

// NodesResult is what the nodes step did.
type NodesResult struct {
	// Paired names the hosts this run paired (or would pair, on --check).
	Paired []string
	// Existing names requested hosts that were already paired.
	Existing []string
	// Discovered names the LAN hosts the scan returned; empty unless the
	// operator opted in.
	Discovered []string
	// Travel is the travel flag recorded on every node paired here.
	Travel bool
}

// nodeTarget is one host to pair with the code that pairs it.
type nodeTarget struct {
	host string
	code string
}

// StepNodes runs the opt-in LAN nodes step over cfg.
//
// Order is the contract: the windows refusal, then the opt-in scan, then
// every code resolved, and only then the first Pair call. A run that
// would fail on its third host therefore writes nothing for its first.
func (w *Wizard) StepNodes(ctx context.Context, cfg initconfig.NodesConfig, lan LANSource, pairer NodePairer) (NodesResult, error) {
	res := NodesResult{Travel: cfg.Travel}
	if !cfg.ScanLAN && len(cfg.Pair) == 0 {
		return res, nil
	}
	if w.deps.GOOS == "windows" {
		return res, cascade.New(cascade.KindUnsupported, windowsNodesHint)
	}
	if pairer == nil || (cfg.ScanLAN && lan == nil) {
		return res, cascade.New(cascade.KindInvalidInput,
			"cascade init: the nodes step needs a node pairer, and a LAN source when scan_lan is set")
	}
	w.say("== nodes")
	known, err := pairer.Paired(ctx)
	if err != nil {
		return res, err
	}
	hosts, err := w.nodeHosts(ctx, cfg, lan, &res)
	if err != nil {
		return res, err
	}
	targets, err := w.nodeTargets(hosts, known, &res)
	if err != nil {
		return res, err
	}
	for _, t := range targets {
		if !w.writing() {
			w.plan("pair node %s (travel %t)", t.host, cfg.Travel)
		} else if err := pairer.Pair(ctx, t.host, t.code, cfg.Travel); err != nil {
			return res, err
		}
		res.Paired = append(res.Paired, t.host)
	}
	return res, nil
}

// nodeHosts merges the configured hosts with the LAN hosts the operator
// accepts. A non-interactive run never pairs a host it only discovered:
// it has no question to ask, so the host is listed and left alone.
func (w *Wizard) nodeHosts(ctx context.Context, cfg initconfig.NodesConfig, lan LANSource, res *NodesResult) ([]string, error) {
	hosts := uniqueHosts(cfg.Pair)
	if !cfg.ScanLAN {
		return hosts, nil
	}
	found, err := lan.Scan(ctx)
	if err != nil {
		return nil, err
	}
	res.Discovered = uniqueHosts(found)
	for _, host := range res.Discovered {
		if containsHost(hosts, host) {
			continue
		}
		w.say("   discovered: %s", host)
		if !w.interactive() {
			continue
		}
		ok, err := w.deps.Prompt.Confirm("Pair "+host+"?", false)
		if err != nil {
			return nil, err
		}
		if ok {
			hosts = append(hosts, host)
		}
	}
	return hosts, nil
}

// nodeTargets resolves every host's code before any pairing write.
func (w *Wizard) nodeTargets(hosts, known []string, res *NodesResult) ([]nodeTarget, error) {
	targets := make([]nodeTarget, 0, len(hosts))
	for _, host := range hosts {
		if containsHost(known, host) {
			res.Existing = append(res.Existing, host)
			w.say("   paired: %s (unchanged)", host)
			continue
		}
		code, err := w.nodeCode(host)
		if err != nil {
			return nil, err
		}
		targets = append(targets, nodeTarget{host: host, code: code})
	}
	return targets, nil
}

// nodeCode reads host's one-use code from CASCADE_INIT_NODE_CODE_<HOST>,
// or, only when an operator is answering, from the prompt.
func (w *Wizard) nodeCode(host string) (string, error) {
	if code, err := initconfig.NodeCodeFor(host, w.deps.Getenv); err == nil {
		return code, nil
	}
	key := initconfig.NodeCodeEnv(host)
	if !w.interactive() || w.deps.Prompt == nil {
		return "", cascade.Newf(cascade.KindInvalidInput, nodesNoCodeMsg, host, key)
	}
	code, err := w.deps.Prompt.Line("Pairing code for "+host, "")
	if err != nil {
		return "", err
	}
	if code = strings.TrimSpace(code); code == "" {
		return "", cascade.Newf(cascade.KindInvalidInput, nodesNoCodeMsg, host, key)
	}
	return code, nil
}

// uniqueHosts trims, drops blanks and duplicates, and keeps first-seen
// order so the operator is asked in the order the file lists hosts.
func uniqueHosts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, h := range in {
		if h = strings.TrimSpace(h); h != "" && !containsHost(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// containsHost reports whether hosts carries host, case-insensitively.
func containsHost(hosts []string, host string) bool {
	for _, h := range hosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}
