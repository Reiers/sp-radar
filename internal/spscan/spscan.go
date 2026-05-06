// Package spscan enumerates and probes Filecoin storage providers.
//
// The flow:
//
//  1. StateListMiners → all addresses ever registered
//  2. StateMinerPower → filter to those with min power (power-table consensus)
//  3. StateMinerInfo  → resolve peerID + multiaddrs per miner
//  4. libp2p connect + identify → agent string + protocol set + IPs
//  5. detect.ClassifyFromSPContext → Software, Version, Role, capabilities
//
// The result is a slice of snapshot.SPRecord ready to drop into a Snapshot.
package spscan

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Reiers/sp-radar/internal/detect"
	"github.com/Reiers/sp-radar/internal/scanner"
	"github.com/Reiers/sp-radar/internal/snapshot"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// Source picks where the active-SP set comes from.
type Source string

const (
	// SourceFilfox uses https://filfox.info/api/v1/miner/list/power to fetch
	// the ~700 currently-active miners with power attached. Fast (~5s, paginated).
	// Default. Recommended for normal snapshots.
	SourceFilfox Source = "filfox"

	// SourceChainFull walks StateListMiners + StateMinerPower across all 750k
	// registered miners. Slow (20-30 min on a local Lotus). Use only when you
	// explicitly want a chain-truth snapshot or Filfox is down.
	SourceChainFull Source = "chain-full"
)

// Options controls a single SPScan run.
type Options struct {
	APIInfo          string
	Source           Source        // "filfox" (default) or "chain-full"
	Concurrency      int           // libp2p dial parallelism
	LotusConcurrency int           // RPC parallelism for the chain-full path
	MaxProviders     int           // 0 = no limit; otherwise cap qualified set
	Timeout          time.Duration // per-peer libp2p timeout
	Verbose          bool

	// OnProgress is called periodically during long phases. Optional.
	OnProgress func(phase string, done, total int64)
}

// Run executes all SP phases and returns the populated SPRecord slice.
// On a clean error (no SPs at all) it returns the underlying err.
// Per-SP errors are recorded inline on the SPRecord via DialError; the
// run continues.
func Run(ctx context.Context, opts Options) ([]snapshot.SPRecord, error) {
	if opts.Concurrency == 0 {
		opts.Concurrency = 50
	}
	if opts.LotusConcurrency == 0 {
		opts.LotusConcurrency = 50
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}
	rpc := scanner.NewLotusRPC(opts.APIInfo)

	h, err := setupHost()
	if err != nil {
		return nil, fmt.Errorf("libp2p host: %w", err)
	}
	defer h.Close()

	if opts.Source == "" {
		opts.Source = SourceFilfox
	}

	var qualified []qualifiedSP
	switch opts.Source {
	case SourceFilfox:
		qualified, err = fetchFilfoxActiveMiners(ctx, opts.OnProgress)
		if err != nil {
			return nil, fmt.Errorf("filfox: %w", err)
		}
	case SourceChainFull:
		miners, err := rpc.StateListMiners(ctx)
		if err != nil {
			return nil, fmt.Errorf("StateListMiners: %w", err)
		}
		progress(opts.OnProgress, "list", int64(len(miners)), int64(len(miners)))
		qualified = filterByPower(ctx, rpc, miners, opts.LotusConcurrency, opts.OnProgress, opts.MaxProviders)
	default:
		return nil, fmt.Errorf("unknown spscan source %q", opts.Source)
	}

	if opts.MaxProviders > 0 && len(qualified) > opts.MaxProviders {
		qualified = qualified[:opts.MaxProviders]
	}

	// Phase 3: probe via libp2p
	records := probeAll(ctx, rpc, h, qualified, opts)
	return records, nil
}

// --- libp2p host setup ---

func setupHost() (host.Host, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".filcensus")
	_ = os.MkdirAll(dir, 0755)
	keyPath := filepath.Join(dir, "libp2p.key")
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	return libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.Identity(key),
	)
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
		out, err := crypto.MarshalPrivateKey(k)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, out, 0600); err != nil {
			return nil, err
		}
		return k, nil
	}
	return crypto.UnmarshalPrivateKey(data)
}

// --- power filter ---

type qualifiedSP struct {
	addr      string
	rawPower  *big.Int
	qualPower *big.Int
}

func filterByPower(ctx context.Context, rpc *scanner.LotusRPC, miners []string, conc int, onProgress func(string, int64, int64), maxQualified int) []qualifiedSP {
	var mu sync.Mutex
	var out []qualifiedSP
	var done atomic.Int64
	total := int64(len(miners))

	localCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	for _, m := range miners {
		if localCtx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			n := done.Add(1)
			if n%5000 == 0 || n == total {
				progress(onProgress, "power-filter", n, total)
			}
			p, err := rpc.StateMinerPower(localCtx, addr)
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
			out = append(out, qualifiedSP{addr: addr, rawPower: raw, qualPower: qual})
			curLen := len(out)
			mu.Unlock()
			// Short-circuit early when we've already got enough qualified SPs.
			// We cancel the local context so in-flight workers stop quickly.
			if maxQualified > 0 && curLen >= maxQualified {
				cancel()
			}
		}(m)
	}
	wg.Wait()
	return out
}

// --- libp2p probe ---

func probeAll(ctx context.Context, rpc *scanner.LotusRPC, h host.Host, sps []qualifiedSP, opts Options) []snapshot.SPRecord {
	out := make([]snapshot.SPRecord, len(sps))
	var done atomic.Int64
	total := int64(len(sps))

	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup

	for i, sp := range sps {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, sp qualifiedSP) {
			defer wg.Done()
			defer func() { <-sem }()

			rec := snapshot.SPRecord{
				MinerID:         sp.addr,
				HasMinPower:     true,
				RawBytePower:    sp.rawPower.String(),
				QualityAdjPower: sp.qualPower.String(),
			}

			pctx, cancel := context.WithTimeout(ctx, opts.Timeout)
			defer cancel()

			info, err := getAddrInfo(pctx, rpc, sp.addr, &rec)
			if err != nil {
				rec.DialError = "addr-info: " + err.Error()
				out[idx] = rec
				if n := done.Add(1); n%50 == 0 || n == total {
					progress(opts.OnProgress, "probe", n, total)
				}
				return
			}

			rec.PeerID = info.ID.String()
			for _, ma := range info.Addrs {
				rec.Multiaddrs = append(rec.Multiaddrs, ma.String())
			}
			rec.IPs = ipsFromMultiaddrs(info.Addrs)

			start := time.Now()
			if err := h.Connect(pctx, *info); err != nil {
				rec.DialError = "connect: " + err.Error()
				rec.DialDuration = time.Since(start)
				out[idx] = rec
				if n := done.Add(1); n%50 == 0 || n == total {
					progress(opts.OnProgress, "probe", n, total)
				}
				return
			}
			rec.DialDuration = time.Since(start)
			rec.Reachable = true

			if ps, err := h.Peerstore().GetProtocols(info.ID); err == nil {
				for _, p := range ps {
					rec.Protocols = append(rec.Protocols, string(p))
				}
			}
			if av, err := h.Peerstore().Get(info.ID, "AgentVersion"); err == nil {
				rec.AgentVersion, _ = av.(string)
			}

			cls := detect.ClassifyFromSPContext(rec.AgentVersion, rec.Protocols)
			rec.Software = string(cls.Software)
			rec.SoftwareVersion = cls.Version
			rec.IndexerCapable = cls.IndexerCapable

			out[idx] = rec
			if n := done.Add(1); n%50 == 0 || n == total {
				progress(opts.OnProgress, "probe", n, total)
			}
		}(i, sp)
	}
	wg.Wait()
	return out
}

func getAddrInfo(ctx context.Context, rpc *scanner.LotusRPC, miner string, rec *snapshot.SPRecord) (*peer.AddrInfo, error) {
	info, err := rpc.StateMinerInfo(ctx, miner)
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

// ipsFromMultiaddrs extracts unique IPv4/IPv6 strings from a multiaddr set.
func ipsFromMultiaddrs(addrs []multiaddr.Multiaddr) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		multiaddr.ForEach(a, func(c multiaddr.Component) bool {
			switch c.Protocol().Code {
			case multiaddr.P_IP4, multiaddr.P_IP6:
				v := c.Value()
				if v != "" && !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
			return true
		})
	}
	return out
}

func progress(fn func(string, int64, int64), phase string, done, total int64) {
	if fn != nil {
		fn(phase, done, total)
	}
}
