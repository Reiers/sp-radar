// Package cluster groups Filecoin storage providers (miner IDs) into
// real-world operators using a union-find over shared identity signals.
//
// Background: a single physical operator typically runs multiple miner IDs
// (one per "lot" of sectors, for legacy / accounting / capacity reasons).
// Reporting at the miner-ID level over-counts the network by ~3-5x. To get
// the real shape of the SP fleet we cluster IDs that share any of:
//
//   - Owner address
//   - Worker address
//   - Any control address
//   - Beneficiary address
//   - Any public IP (with a CDN-noise cap; an IP shared by >50 miners is
//     treated as a shared CDN/proxy, not co-location, and skipped)
//
// This mirrors the approach used in our 2026-Q1 SP census report
// (filecoin-sp-census-public.html), which produced the headline:
//
//   "37 entities run 90% of Filecoin's storage. 5 entities run half."
//   "728 reported miner IDs \u2192 240 active \u2192 73 unique operators"
package cluster

import (
	"sort"
)

// Identity is the per-miner signal we cluster on. Empty fields are ignored.
type Identity struct {
	MinerID         string
	Owner           string
	Worker          string
	Control         []string
	Beneficiary     string
	IPs             []string
	RawBytePower    string // big-int string; we only sort/sum, never compare arithmetic
	QualityAdjPower string
}

// Cluster is one group of miner IDs that share at least one identity signal.
type Cluster struct {
	Representative  string   // canonical miner ID (smallest f0... in the cluster)
	Members         []string // sorted ascending
	Owners          []string // unique, sorted
	Workers         []string // unique, sorted
	Beneficiaries   []string // unique, sorted
	IPs             []string // unique, sorted
	RawBytePower    string   // sum of raw bytes (big-int decimal string)
	QualityAdjPower string   // sum of QA bytes
}

// SharedIPCap caps the number of miners that may share an IP before that IP
// is treated as a CDN/proxy and ignored for clustering. Mirrors the legacy
// sp_census.py threshold (50). Clusters built from CDN/proxy IPs would be
// false positives ("everyone behind Cloudflare is the same operator" \u2014 no).
const SharedIPCap = 50

// Cluster runs union-find over the input identities and returns one Cluster
// per group, sorted by RawBytePower descending.
func Build(input []Identity) []Cluster {
	if len(input) == 0 {
		return nil
	}

	// Index inputs by MinerID for later metadata lookup.
	byID := make(map[string]Identity, len(input))
	for _, in := range input {
		byID[in.MinerID] = in
	}

	parent := make(map[string]string, len(input))
	for id := range byID {
		parent[id] = id
	}
	var find func(string) string
	find = func(x string) string {
		for parent[x] != x {
			// path compression
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	// Group by signal value.
	ownerMap := map[string][]string{}
	workerMap := map[string][]string{}
	ctrlMap := map[string][]string{}
	benefMap := map[string][]string{}
	ipMap := map[string][]string{}

	for id, in := range byID {
		if in.Owner != "" {
			ownerMap[in.Owner] = append(ownerMap[in.Owner], id)
		}
		if in.Worker != "" {
			workerMap[in.Worker] = append(workerMap[in.Worker], id)
		}
		for _, c := range in.Control {
			if c != "" {
				ctrlMap[c] = append(ctrlMap[c], id)
			}
		}
		if in.Beneficiary != "" {
			benefMap[in.Beneficiary] = append(benefMap[in.Beneficiary], id)
		}
		for _, ip := range in.IPs {
			if ip != "" {
				ipMap[ip] = append(ipMap[ip], id)
			}
		}
	}

	for _, group := range []map[string][]string{ownerMap, workerMap, ctrlMap, benefMap} {
		for _, members := range group {
			for i := 1; i < len(members); i++ {
				union(members[0], members[i])
			}
		}
	}
	// IP map gets the CDN cap.
	for _, members := range ipMap {
		if len(members) > SharedIPCap {
			continue
		}
		for i := 1; i < len(members); i++ {
			union(members[0], members[i])
		}
	}

	// Materialise clusters.
	groups := map[string][]string{}
	for id := range byID {
		root := find(id)
		groups[root] = append(groups[root], id)
	}

	clusters := make([]Cluster, 0, len(groups))
	for _, members := range groups {
		sort.Strings(members)
		c := Cluster{
			Representative: members[0],
			Members:        members,
		}
		// Aggregate metadata + power.
		uniq := func(s []string) []string {
			m := map[string]struct{}{}
			for _, x := range s {
				if x != "" {
					m[x] = struct{}{}
				}
			}
			out := make([]string, 0, len(m))
			for k := range m {
				out = append(out, k)
			}
			sort.Strings(out)
			return out
		}
		var owners, workers, benefs, ips []string
		var raw, qa bigSum
		for _, id := range members {
			in := byID[id]
			owners = append(owners, in.Owner)
			workers = append(workers, in.Worker)
			benefs = append(benefs, in.Beneficiary)
			ips = append(ips, in.IPs...)
			raw.Add(in.RawBytePower)
			qa.Add(in.QualityAdjPower)
		}
		c.Owners = uniq(owners)
		c.Workers = uniq(workers)
		c.Beneficiaries = uniq(benefs)
		c.IPs = uniq(ips)
		c.RawBytePower = raw.String()
		c.QualityAdjPower = qa.String()
		clusters = append(clusters, c)
	}

	// Sort by raw power descending. Stable secondary by representative ID.
	sort.SliceStable(clusters, func(i, j int) bool {
		ri := bigCmp(clusters[i].RawBytePower, clusters[j].RawBytePower)
		if ri != 0 {
			return ri > 0
		}
		return clusters[i].Representative < clusters[j].Representative
	})
	return clusters
}
