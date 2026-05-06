// Package chaincrawl discovers Filecoin chain nodes (Lotus / Forest / Venus
// full nodes) by walking the libp2p peer graph from our own node.
//
// The cheapest method is also the most reliable for this purpose:
//
//   1. Filecoin.NetPeers returns the daemon's directly-connected peers.
//   2. Filecoin.NetAgentVersion returns the agent string for each.
//   3. We classify via internal/detect.
//
// Walking deeper (peer-of-peer, DHT crawl) is possible but produces a lot
// of noise (ipfs network nodes, gateways, etc.). The directly-connected set
// is already a good cross-section of the public-facing chain-node fleet.
package chaincrawl

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/Reiers/sp-radar/internal/detect"
	"github.com/Reiers/sp-radar/internal/scanner"
	"github.com/Reiers/sp-radar/internal/snapshot"
)

// Options controls a chain-node crawl run.
type Options struct {
	APIInfo         string
	AgentConcurrency int  // parallel NetAgentVersion calls
	OnProgress      func(done, total int64)
}

// Run discovers chain nodes via NetPeers + NetAgentVersion and returns a
// slice of ChainNodeRecord rows.
func Run(ctx context.Context, opts Options) ([]snapshot.ChainNodeRecord, error) {
	if opts.AgentConcurrency == 0 {
		opts.AgentConcurrency = 16
	}
	rpc := scanner.NewLotusRPC(opts.APIInfo)

	peers, err := rpc.NetPeers(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]snapshot.ChainNodeRecord, len(peers))
	var done atomic.Int64
	total := int64(len(peers))

	sem := make(chan struct{}, opts.AgentConcurrency)
	var wg sync.WaitGroup

	for i, p := range peers {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, peer scanner.NetPeer) {
			defer wg.Done()
			defer func() { <-sem }()

			rec := snapshot.ChainNodeRecord{
				PeerID:     peer.ID,
				Multiaddrs: append([]string(nil), peer.Addrs...),
				Reachable:  true, // by definition: we're connected to it
			}
			rec.IPs = ipsFromMultiaddrStrings(peer.Addrs)

			if av, err := rpc.NetAgentVersion(ctx, peer.ID); err == nil {
				rec.AgentVersion = av
				cls := detect.Classify(av, nil)
				rec.Software = string(cls.Software)
				rec.SoftwareVersion = cls.Version
			}
			// Note: protocol enumeration would require a separate libp2p dial
			// from our process. That is more expensive and we already have a
			// reasonable signal via the agent string. Skipped for v1.

			out[idx] = rec

			if n := done.Add(1); opts.OnProgress != nil && (n%50 == 0 || n == total) {
				opts.OnProgress(n, total)
			}
		}(i, p)
	}
	wg.Wait()
	return out, nil
}

// ipsFromMultiaddrStrings parses a list of multiaddr strings and returns a
// dedup'd list of IPv4/IPv6 components. Lightweight string-based version of
// what spscan does (chain-node multiaddrs come as strings from Filecoin.NetPeers).
func ipsFromMultiaddrStrings(addrs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		// Multiaddr format: /ip4/x.x.x.x/tcp/yyyy or /ip6/...
		// Cheap parse: split on "/" and look for ip4 / ip6 prefixes.
		parts := splitMaddr(a)
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == "ip4" || parts[i] == "ip6" {
				v := parts[i+1]
				if v != "" && !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func splitMaddr(s string) []string {
	if len(s) == 0 || s[0] != '/' {
		return nil
	}
	// Trim leading '/' so the split yields clean tokens.
	s = s[1:]
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
