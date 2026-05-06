// Package detect parses libp2p identify "AgentVersion" strings and supported
// protocol lists into a (Software, Version, Role) triple.
//
// All upstream signature sources are cited in the relevant cases and were
// verified against the upstream repos on 2026-05-06. See SCOPE.md for details.
//
// This package is intentionally pure: no network, no I/O. Easy to unit test.
package detect

import (
	"regexp"
	"strings"
)

// Software identifies a Filecoin daemon implementation.
type Software string

const (
	// Storage providers (the SP fleet on chain)
	Curio       Software = "curio"        // Curio storage stack (modern)
	Boost       Software = "boost"        // Boost storage market
	LotusMiner  Software = "lotus-miner"  // Legacy lotus-miner SP daemon
	VenusMiner  Software = "venus-miner"  // Venus miner daemon
	Droplet     Software = "droplet"      // Venus droplet (storage/retrieval, ex-market)
	Markets     Software = "markets"      // Legacy go-fil-markets storage daemon

	// Chain / RPC nodes (the consensus layer)
	Lotus  Software = "lotus"  // Lotus full node
	Forest Software = "forest" // ChainSafe Forest full node (Rust)
	Venus  Software = "venus"  // Venus full node

	// Catch-all
	Unknown Software = "unknown"

	// Reachability buckets used by the SP collector to distinguish *why* a
	// node is unclassified. These are not real software identifiers; they
	// describe collection outcome. Surfaced as their own labels in the
	// dashboard so "we couldn't reach them" doesn't get conflated with
	// "we reached them but didn't recognize the software".
	SoftwareNoPeerID Software = "no-peer-id"  // chain has no peer ID for this miner
	SoftwarePrivate  Software = "private"     // peer ID published, but we couldn't dial
	SoftwareOther    Software = "other"       // dialed and got an agent string we don't recognize
)

// Role is the broad category a node falls into. One signature can classify
// to multiple roles in principle (e.g. an SP also runs a chain node), but a
// single libp2p identify response represents one daemon, so one role.
type Role string

const (
	RoleSP        Role = "sp"         // Storage provider (Curio/Boost/lotus-miner/venus-miner/droplet/markets)
	RoleChainNode Role = "chain-node" // Full chain node (Lotus/Forest/Venus)
	RoleUnknown   Role = "unknown"
)

// Result is the parsed classification of an observed peer.
type Result struct {
	Software Software
	Version  string // Best-effort, may be ""
	Role     Role

	// AgentRaw is the raw libp2p AgentVersion string we classified from.
	// Useful for debugging / triage when classification falls through to Unknown.
	AgentRaw string

	// IndexerCapable is true when the peer announces /legs/head/ or related
	// protocols indicating IPNI / network indexer participation.
	IndexerCapable bool

	// SupportsBoostMarket / SupportsLegacyMarket are protocol hints.
	SupportsBoostMarket  bool
	SupportsLegacyMarket bool
}

// Classify returns a Result given an agent string and the peer's announced
// protocol set. Either may be empty; we do best-effort detection.
func Classify(agent string, protocols []string) Result {
	r := Result{
		AgentRaw: agent,
		Software: Unknown,
		Role:     RoleUnknown,
	}
	r.IndexerCapable = hasProtoContains(protocols, "/legs/head/") ||
		hasProtoContains(protocols, "/indexer/ingest/") ||
		hasProtoContains(protocols, "/fil/index/")
	r.SupportsBoostMarket = hasProtoContains(protocols, "/fil/storage/mk/1.2.0")
	r.SupportsLegacyMarket = hasProtoContains(protocols, "/fil/storage/mk/1.1.0")

	r.Software, r.Version = parseAgent(agent)

	switch r.Software {
	case Lotus, Forest, Venus:
		r.Role = RoleChainNode
	case Curio, Boost, LotusMiner, VenusMiner, Droplet, Markets:
		r.Role = RoleSP
	}

	// Protocol-only fallback: agent unparseable but markets protocols hint role
	if r.Software == Unknown {
		switch {
		case r.SupportsBoostMarket:
			r.Software = Boost
			r.Role = RoleSP
		case r.SupportsLegacyMarket:
			r.Software = Markets
			r.Role = RoleSP
		}
	}

	return r
}

// --- Agent string parsing ---

// Reference signatures (verified upstream on 2026-05-06):
//
//   Lotus  build/buildconstants/params.go:42  const UserAgent = "lotus"
//          libp2p UserAgent format:           "lotus-<version>"  (sometimes "lotus-<ver>+mainnet+git_<hash>")
//   Forest src/libp2p/discovery.rs:183        format!("forest-{version}")
//          actual format:                      "forest-<ver>+git.<hash>"
//   Venus  app/submodule/network/network_submodule.go:382  libp2p.UserAgent("venus")
//          actual format:                      bare "venus" or "venus-<ver>" depending on build flags
//   Curio  curio repo libp2p init             "curio-<version>"
//   Boost  boostd libp2p init                 "boost-<version>"
//
// Important: lotus full node and lotus-miner share the SAME prefix ("lotus-"),
// the libp2p layer doesn't distinguish them. We use protocol set + chain context
// to decide, so a bare "lotus-1.34" we encounter via libp2p with no markets
// protocols defaults to Lotus (full node). When called from the SP enumeration
// path (where we already know it's a chain-listed miner), the caller should
// override Software to LotusMiner. See ClassifyFromSPContext below.

var (
	// Splits "<name>-<version>" with optional "+suffix"
	reAgent = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9_-]*?)[/-]([0-9][^+\s]*)`)
)

func parseAgent(agent string) (Software, string) {
	a := strings.TrimSpace(agent)
	if a == "" {
		return Unknown, ""
	}
	lo := strings.ToLower(a)

	// Bare strings that some implementations actually publish unversioned.
	switch lo {
	case "venus":
		return Venus, ""
	case "lotus":
		return Lotus, ""
	case "forest":
		return Forest, ""
	}

	// Substring checks first (handles funky formats like "venus-miner/v1.x" and "droplet-v1.x")
	switch {
	case strings.Contains(lo, "venus-miner"):
		return VenusMiner, extractVersion(a)
	case strings.Contains(lo, "droplet"):
		return Droplet, extractVersion(a)
	case strings.Contains(lo, "venus"):
		return Venus, extractVersion(a)
	case strings.Contains(lo, "forest"):
		return Forest, extractVersion(a)
	case strings.Contains(lo, "curio"):
		return Curio, extractVersion(a)
	case strings.Contains(lo, "boost"):
		return Boost, extractVersion(a)
	case strings.Contains(lo, "lotus-miner") || strings.Contains(lo, "lotus_miner"):
		return LotusMiner, extractVersion(a)
	case strings.Contains(lo, "lotus"):
		// Default lotus-prefixed string to Lotus full node. Caller can override
		// to LotusMiner via ClassifyFromSPContext when chain context says so.
		return Lotus, extractVersion(a)
	}
	return Unknown, ""
}

// extractVersion pulls a semver-ish substring out of an agent string.
// "lotus-1.34.1+mainnet+git_abc" -> "1.34.1"
// "forest-0.30.0+git.abc"        -> "0.30.0"
// "boost/1.7.6"                  -> "1.7.6"
// "boost-1.7.6-rc1"              -> "1.7.6-rc1"
func extractVersion(agent string) string {
	m := reAgent.FindStringSubmatch(agent)
	if len(m) < 3 {
		return ""
	}
	v := m[2]
	// Strip a trailing "+..." suffix if regex didn't already.
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	return v
}

// ClassifyFromSPContext is like Classify but used when the caller already
// knows the peer is a chain-listed storage provider (i.e. enumerated from
// StateListMiners). In that case a bare "lotus-<ver>" agent is reclassified
// as LotusMiner instead of Lotus, since a full-node chain daemon would not
// be the registered miner peer ID for an SP.
func ClassifyFromSPContext(agent string, protocols []string) Result {
	r := Classify(agent, protocols)
	if r.Software == Lotus {
		r.Software = LotusMiner
		r.Role = RoleSP
	}
	return r
}

// --- protocol helpers ---

func hasProtoContains(protos []string, sub string) bool {
	for _, p := range protos {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}
