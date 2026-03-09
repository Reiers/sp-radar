package scanner

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type ScanOptions struct {
	APIInfo          string
	Concurrency      int
	LotusConcurrency int
	MaxProviders     int
	Verbose          bool
	Timeout          time.Duration
}

type SoftwareStats struct {
	Count                int     `json:"count"`
	RawPowerBytes        string  `json:"raw_power_bytes"`
	QualityAdjPowerBytes string  `json:"quality_adj_power_bytes"`
	RawPowerPiB          float64 `json:"raw_power_pib"`
	QualityAdjPowerPiB   float64 `json:"quality_adj_power_pib"`
}

type SoftwareDistribution struct {
	Curio   SoftwareStats `json:"curio"`
	Boost   SoftwareStats `json:"boost"`
	Venus   SoftwareStats `json:"venus"`
	Markets SoftwareStats `json:"markets"`
	Unknown SoftwareStats `json:"unknown"`
}

type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ScanResult struct {
	Timestamp            time.Time            `json:"timestamp"`
	TotalSPsOnChain      int                  `json:"total_sps_on_chain"`
	TotalSPsWithMinPower int                  `json:"total_sps_with_min_power"`
	TotalSPsScanned      int                  `json:"total_sps_scanned"`
	Software             SoftwareDistribution `json:"software"`
	IndexerNodes         int                  `json:"indexer_nodes"`
	AgentVersions        map[string]int       `json:"agent_versions"`
	RetrievalProtocols   map[string]int       `json:"retrieval_protocols"`
}

func (r *ScanResult) AgentVersionsSorted() []NameCount { return sortMap(r.AgentVersions) }
func (r *ScanResult) RetrievalProtocolsSorted() []NameCount { return sortMap(r.RetrievalProtocols) }

func sortMap(m map[string]int) []NameCount {
	out := make([]NameCount, 0, len(m))
	for k, v := range m {
		out = append(out, NameCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

type minerPower struct {
	addr      string
	rawPower  *big.Int
	qualPower *big.Int
}

// Scan runs a full network scan.
func Scan(ctx context.Context, opts ScanOptions) (*ScanResult, error) {
	rpc := NewLotusRPC(opts.APIInfo)

	h, err := setupHost()
	if err != nil {
		return nil, fmt.Errorf("libp2p setup: %w", err)
	}
	defer h.Close()

	// Phase 1: list miners
	fmt.Fprintf(os.Stderr, "Fetching miner list...\n")
	miners, err := rpc.StateListMiners(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing miners: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Total SPs on chain: %d\n", len(miners))

	// Phase 2: filter by min power
	fmt.Fprintf(os.Stderr, "Checking power (concurrency: %d)...\n", opts.LotusConcurrency)
	qualified := filterMinPower(ctx, rpc, miners, opts.LotusConcurrency)
	fmt.Fprintf(os.Stderr, "SPs with minimum power: %d\n", len(qualified))

	if opts.MaxProviders > 0 && len(qualified) > opts.MaxProviders {
		qualified = qualified[:opts.MaxProviders]
		fmt.Fprintf(os.Stderr, "Capped to %d providers\n", opts.MaxProviders)
	}

	// Phase 3: scan via libp2p
	fmt.Fprintf(os.Stderr, "Scanning via libp2p (concurrency: %d)...\n", opts.Concurrency)
	result := &ScanResult{
		Timestamp:            time.Now().UTC(),
		TotalSPsOnChain:      len(miners),
		TotalSPsWithMinPower: len(qualified),
		AgentVersions:        make(map[string]int),
		RetrievalProtocols:   make(map[string]int),
	}
	scanAll(ctx, rpc, h, qualified, opts, result)

	for _, s := range []*SoftwareStats{
		&result.Software.Curio, &result.Software.Boost, &result.Software.Venus,
		&result.Software.Markets, &result.Software.Unknown,
	} {
		toPiB(s)
	}
	result.TotalSPsScanned = result.Software.Curio.Count + result.Software.Boost.Count +
		result.Software.Venus.Count + result.Software.Markets.Count + result.Software.Unknown.Count
	return result, nil
}

// --- libp2p host ---

func setupHost() (host.Host, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".sp-radar")
	os.MkdirAll(dir, 0755)
	key, err := loadOrCreateKey(filepath.Join(dir, "libp2p.key"))
	if err != nil {
		return nil, err
	}
	return libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"), libp2p.Identity(key))
}

func loadOrCreateKey(path string) (crypto.PrivKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		k, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return nil, err
		}
		data, err = crypto.MarshalPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return k, os.WriteFile(path, data, 0600)
	}
	return crypto.UnmarshalPrivateKey(data)
}

// --- phase 2: power filter ---

func filterMinPower(ctx context.Context, rpc *LotusRPC, miners []string, conc int) []minerPower {
	var mu sync.Mutex
	var out []minerPower
	var done atomic.Int64
	total := len(miners)
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	for _, m := range miners {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			if n := done.Add(1); n%5000 == 0 {
				fmt.Fprintf(os.Stderr, "  Power: %d/%d (qualified: %d)\n", n, total, len(out))
			}
			p, err := rpc.StateMinerPower(ctx, addr)
			if err != nil || !p.HasMinPower {
				return
			}
			raw, _ := new(big.Int).SetString(p.MinerPower.RawBytePower, 10)
			qual, _ := new(big.Int).SetString(p.MinerPower.QualityAdjPower, 10)
			if raw == nil {
				raw = new(big.Int)
			}
			if qual == nil {
				qual = new(big.Int)
			}
			mu.Lock()
			out = append(out, minerPower{addr: addr, rawPower: raw, qualPower: qual})
			mu.Unlock()
		}(m)
	}
	wg.Wait()
	return out
}

// --- phase 3: libp2p scan ---

func scanAll(ctx context.Context, rpc *LotusRPC, h host.Host, providers []minerPower, opts ScanOptions, result *ScanResult) {
	var mu sync.Mutex
	var done atomic.Int64
	total := len(providers)

	acc := map[string][2]*big.Int{
		"curio": {new(big.Int), new(big.Int)}, "boost": {new(big.Int), new(big.Int)},
		"venus": {new(big.Int), new(big.Int)}, "markets": {new(big.Int), new(big.Int)},
		"unknown": {new(big.Int), new(big.Int)},
	}

	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup

	for _, mp := range providers {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(mp minerPower) {
			defer wg.Done()
			defer func() { <-sem }()
			if n := done.Add(1); n%50 == 0 || n == int64(total) {
				fmt.Fprintf(os.Stderr, "  Scan: %d/%d\n", n, total)
			}

			sctx, cancel := context.WithTimeout(ctx, opts.Timeout)
			defer cancel()

			sw := "unknown"
			var agent string
			var protos []string

			if info, err := getAddrInfo(sctx, rpc, mp.addr); err == nil {
				if err := h.Connect(sctx, *info); err == nil {
					if ps, err := h.Peerstore().GetProtocols(info.ID); err == nil {
						for _, p := range ps {
							protos = append(protos, string(p))
						}
					}
					if av, err := h.Peerstore().Get(info.ID, "AgentVersion"); err == nil {
						agent, _ = av.(string)
					}
					sw = classify(agent, protos)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			switch sw {
			case "curio":
				result.Software.Curio.Count++
			case "boost":
				result.Software.Boost.Count++
				result.AgentVersions[shortAgent(agent)]++
			case "venus":
				result.Software.Venus.Count++
			case "markets":
				result.Software.Markets.Count++
				result.AgentVersions[shortAgent(agent)]++
			default:
				result.Software.Unknown.Count++
			}
			acc[sw][0].Add(acc[sw][0], mp.rawPower)
			acc[sw][1].Add(acc[sw][1], mp.qualPower)

			if hasProto(protos, "/legs/head/") {
				result.IndexerNodes++
			}
			if opts.Verbose {
				fmt.Printf("%s agent=%q sw=%s\n", mp.addr, agent, sw)
			}
		}(mp)
	}
	wg.Wait()

	for _, name := range []string{"curio", "boost", "venus", "markets", "unknown"} {
		s := getSW(result, name)
		s.RawPowerBytes = acc[name][0].String()
		s.QualityAdjPowerBytes = acc[name][1].String()
	}
}

func getSW(r *ScanResult, name string) *SoftwareStats {
	switch name {
	case "curio":
		return &r.Software.Curio
	case "boost":
		return &r.Software.Boost
	case "venus":
		return &r.Software.Venus
	case "markets":
		return &r.Software.Markets
	default:
		return &r.Software.Unknown
	}
}

func getAddrInfo(ctx context.Context, rpc *LotusRPC, maddr string) (*peer.AddrInfo, error) {
	info, err := rpc.StateMinerInfo(ctx, maddr)
	if err != nil {
		return nil, err
	}
	if info.PeerId == "" {
		return nil, fmt.Errorf("no peer ID")
	}
	pid, err := peer.Decode(info.PeerId)
	if err != nil {
		return nil, err
	}
	var addrs []multiaddr.Multiaddr
	for _, raw := range info.Multiaddrs {
		if ma, err := multiaddr.NewMultiaddrBytes(raw); err == nil {
			addrs = append(addrs, ma)
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addrs")
	}
	return &peer.AddrInfo{ID: pid, Addrs: addrs}, nil
}

// --- detection ---

func classify(agent string, protos []string) string {
	lo := strings.ToLower(agent)
	switch {
	case strings.Contains(lo, "curio"):
		return "curio"
	case strings.Contains(lo, "venus") || strings.Contains(lo, "droplet"):
		return "venus"
	case hasProto(protos, "/fil/storage/mk/1.2.0"):
		return "boost"
	case hasProto(protos, "/fil/storage/mk/1.1.0"):
		return "markets"
	default:
		return "unknown"
	}
}

func hasProto(protos []string, sub string) bool {
	for _, p := range protos {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

var reAgent = regexp.MustCompile(`^(.+)\+.+$`)

func shortAgent(av string) string { return reAgent.ReplaceAllString(av, "$1") }

func toPiB(s *SoftwareStats) {
	div := new(big.Float).SetFloat64(1125899906842624) // 1 PiB
	b := new(big.Float)
	b.SetString(s.RawPowerBytes)
	s.RawPowerPiB, _ = new(big.Float).Quo(b, div).Float64()
	q := new(big.Float)
	q.SetString(s.QualityAdjPowerBytes)
	s.QualityAdjPowerPiB, _ = new(big.Float).Quo(q, div).Float64()
}
