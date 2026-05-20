// Package upgrade classifies network nodes by their readiness for an upcoming
// Filecoin network upgrade.
//
// The classification works on top of a Snapshot (chain nodes + SP records as
// produced by internal/chaincrawl + internal/spscan). It compares each node's
// detected (software, version) against per-implementation minimum-version
// requirements for the target nv, and returns a per-implementation breakdown
// plus an overall aggregate.
//
// This package is pure logic: no network, no I/O.
package upgrade

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Reiers/sp-radar/internal/detect"
	"github.com/Reiers/sp-radar/internal/snapshot"
)

// MinVersion is a per-implementation minimum-version rule.
type MinVersion struct {
	Software  detect.Software
	MinSemver string // e.g. "1.36.0"
	// Pending indicates the implementation hasn't shipped a mainnet release
	// for the target upgrade yet. The bucket will be marked pending instead
	// of generating a misleading 0% ready number.
	Pending bool
}

// NV28FireHorse is the readiness rule set for Filecoin network version 28
// "Fire Horse", activating on mainnet at epoch 6,052,800 / 2026-05-27T14:00Z.
//
// Sources verified 2026-05-20 against each project's GitHub releases that
// explicitly mention nv28 / network version 28 / Fire Horse:
//
//	Lotus       : v1.36.0 is the MANDATORY mainnet release for nv28.
//	              v1.36.0-rc1 also works (release notes say so explicitly).
//	              Source: github.com/filecoin-project/lotus/releases/v1.36.0
//	Lotus-miner : same release tag prefix (miner/v1.36.0).
//	Curio       : v1.28.0 is the MANDATORY mainnet release for nv28.
//	              Source: github.com/filecoin-project/curio/releases/v1.28.0
//	Forest      : v0.33.4 is the MANDATORY mainnet release for nv28. Earlier
//	              0.33.x releases only ship calibnet / devnet support; v0.32.4
//	              was just the internal "nv28 skeleton" PR. Corrected after
//	              Hubert Bugaj (ChainSafe Forest) flagged this in Slack.
//	              Source: github.com/ChainSafe/forest/releases/v0.33.4
//	Venus       : NO MAINNET RELEASE YET as of 2026-05-20. v1.20.0 ships
//	              calibnet activation only. Venus is included in the rule
//	              set with a placeholder minimum, but the UI/footnote
//	              should surface this as "awaiting mainnet release" rather
//	              than a 0% readiness number that misrepresents Venus.
//	              Source: github.com/filecoin-project/venus/releases/v1.20.0
//
// Boost / Droplet / Markets are deal-market layers and do not gate on the
// consensus network version — they're tracked separately as informational
// (their readiness depends on the underlying full node).
var NV28FireHorse = []MinVersion{
	{Software: detect.Lotus, MinSemver: "1.36.0"},
	{Software: detect.LotusMiner, MinSemver: "1.36.0"},
	{Software: detect.Curio, MinSemver: "1.28.0"},
	{Software: detect.Forest, MinSemver: "0.33.4"},
	// Venus minimum is provisional; no mainnet release at time of writing.
	// We surface this state via the Pending flag on the Bucket so the UI
	// shows "awaiting mainnet release" instead of 0% ready.
	{Software: detect.Venus, MinSemver: "99.99.99", Pending: true},
}

// Bucket holds counts for one software implementation.
type Bucket struct {
	Software   string `json:"software"`
	MinVersion string `json:"minVersion"`
	Total      int    `json:"total"`
	Ready      int    `json:"ready"`
	NotReady   int    `json:"notReady"`
	Unknown    int    `json:"unknown"`
	// Pending is true when no mainnet release for this upgrade exists yet.
	// In that case Ready/NotReady are not meaningful and the UI should
	// surface "awaiting mainnet release" instead of a 0% bar.
	Pending bool `json:"pending,omitempty"`
	// TopVersions is a list of (version, count) for the most common versions
	// seen on this software. Sorted by count desc, capped at 8.
	TopVersions []VersionCount `json:"topVersions"`
}

// VersionCount is one (version, count, ready) row in a per-software breakdown.
type VersionCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
	Ready   bool   `json:"ready"`
}

// Readiness is the full per-upgrade readiness report.
type Readiness struct {
	UpgradeName    string    `json:"upgradeName"`
	NetworkVersion int       `json:"networkVersion"`
	SnapshotTime   time.Time `json:"snapshotTime"`

	// Aggregate over all classified peers (excluding `boost`, `markets`, etc.
	// that don't gate on nv).
	TotalPeers int `json:"totalPeers"`
	Ready      int `json:"ready"`
	NotReady   int `json:"notReady"`
	Unknown    int `json:"unknown"`

	// Per-implementation breakdown.
	Buckets []Bucket `json:"buckets"`

	// Other peers we observed but that don't gate on nv (boost, markets,
	// unknown agent strings, etc.) — surfaced for completeness.
	OtherPeers int `json:"otherPeers"`
}

// bucketBuilder is internal classification scratch state.
type bucketBuilder struct {
	min      string
	versions map[string]int
}

// Classify walks a Snapshot's chain nodes and SP records and returns a
// readiness report for the given minimum-version rules.
func Classify(s *snapshot.Snapshot, rules []MinVersion) Readiness {
	r := Readiness{
		UpgradeName:    "Fire Horse",
		NetworkVersion: 28,
		SnapshotTime:   s.GeneratedAt,
	}

	builders := map[detect.Software]*bucketBuilder{}
	for _, m := range rules {
		builders[m.Software] = &bucketBuilder{
			min:      m.MinSemver,
			versions: map[string]int{},
		}
		min := m.MinSemver
		if m.Pending {
			min = "pending"
		}
		r.Buckets = append(r.Buckets, Bucket{
			Software:   string(m.Software),
			MinVersion: min,
			Pending:    m.Pending,
		})
	}

	// Walk chain nodes (Lotus/Forest/Venus full nodes)
	for _, n := range s.ChainNodes {
		sw := detect.Software(n.Software)
		classifyOne(r.Buckets, builders, sw, n.SoftwareVersion, &r)
	}
	// Walk SP records. SPRecord.Software is the libp2p-detected daemon
	// (curio / lotus-miner / boost / etc.) when the dial succeeded; it can
	// also be a reachability bucket (no-peer-id / private / other) when not.
	for _, sp := range s.SPs {
		sw := detect.Software(sp.Software)
		if sw == detect.SoftwareNoPeerID || sw == detect.SoftwarePrivate {
			continue
		}
		classifyOne(r.Buckets, builders, sw, sp.SoftwareVersion, &r)
	}

	// Flush builders -> Buckets (top versions)
	for i, b := range r.Buckets {
		bb := builders[detect.Software(b.Software)]
		if bb == nil {
			continue
		}
		topVer := make([]VersionCount, 0, len(bb.versions))
		for v, cnt := range bb.versions {
			topVer = append(topVer, VersionCount{
				Version: v,
				Count:   cnt,
				Ready:   meetsMinimum(v, bb.min),
			})
		}
		sort.SliceStable(topVer, func(i, j int) bool {
			if topVer[i].Count != topVer[j].Count {
				return topVer[i].Count > topVer[j].Count
			}
			return topVer[i].Version < topVer[j].Version
		})
		if len(topVer) > 8 {
			topVer = topVer[:8]
		}
		r.Buckets[i].TopVersions = topVer
	}

	return r
}

// classifyOne is the inner classifier. It mutates the buckets slice in place.
func classifyOne(buckets []Bucket, builders map[detect.Software]*bucketBuilder, sw detect.Software, ver string, r *Readiness) {
	bb, ok := builders[sw]
	if !ok {
		r.OtherPeers++
		return
	}
	idx := -1
	for i, b := range buckets {
		if b.Software == string(sw) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}

	buckets[idx].Total++

	// Pending bucket: count nodes for context but DON'T fold into the
	// aggregate ready/notReady totals, since no mainnet release exists yet.
	if buckets[idx].Pending {
		ver = strings.TrimSpace(ver)
		if ver == "" {
			bb.versions["(unknown)"]++
		} else {
			bb.versions[ver]++
		}
		return
	}

	r.TotalPeers++

	ver = strings.TrimSpace(ver)
	if ver == "" {
		buckets[idx].Unknown++
		r.Unknown++
		bb.versions["(unknown)"]++
		return
	}
	bb.versions[ver]++

	if meetsMinimum(ver, bb.min) {
		buckets[idx].Ready++
		r.Ready++
	} else {
		buckets[idx].NotReady++
		r.NotReady++
	}
}

// meetsMinimum compares two semver-ish version strings and returns whether
// `have` >= `min`. The comparison ignores any "v" prefix, pre-release/build
// suffixes (so "1.36.0-rc1" >= "1.36.0" returns true on the (1,36,0) numeric
// triple — this matches our intent: an RC built against the right consensus
// version is "ready", even though strict semver would order RCs below the
// final release).
//
// This is deliberately permissive: better to call a 1.36.0-rc1 node "ready"
// than "not ready" for the purposes of network-wide readiness dashboarding.
func meetsMinimum(have, min string) bool {
	h := parseTriple(have)
	m := parseTriple(min)
	for i := 0; i < 3; i++ {
		if h[i] != m[i] {
			return h[i] > m[i]
		}
	}
	return true
}

// parseTriple strips a leading "v" and any pre-release/build suffix, returning
// the (major, minor, patch) tuple as ints. Missing components become 0.
//
// Examples:
//
//	"v1.36.0"          -> (1, 36, 0)
//	"1.36.0-rc1"       -> (1, 36, 0)
//	"1.34.1_sdminer_v1"-> (1, 34, 1)
//	"0.32.4 \"Mild\""  -> (0, 32, 4)
//	""                 -> (0, 0, 0)
//
// We're conservative: anything we can't parse cleanly returns (0, 0, 0),
// which puts it in the not-ready bucket. The dashboard exposes the raw
// agent string so triage is easy.
func parseTriple(v string) [3]int {
	out := [3]int{}
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	// Cut at first non-version-y character.
	end := len(v)
	for i, c := range v {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '.' {
			continue
		}
		end = i
		break
	}
	v = v[:end]
	parts := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return out
		}
		out[i] = n
	}
	return out
}
