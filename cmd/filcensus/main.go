// filcensus is the new entry point: enumerates SPs, FoC providers, and chain
// nodes, enriches with GeoIP, and writes one snapshot per run.
//
// Default flow (no flags):
//
//   1. Connect to Lotus mainnet via FULLNODE_API_INFO
//   2. Enumerate SPs (StateListMiners + StateMinerInfo + StateMinerPower)
//   3. Probe each SP via libp2p identify
//   4. Enumerate FoC providers from ServiceProviderRegistry
//   5. HTTP-probe FoC serviceURLs at /pdp/ping
//   6. GeoIP-enrich resolved IPs
//   7. Write snapshots/<network>-<YYYY-MM-DD>.json
//
// Flags let you skip phases, limit scope (--max-sps, --foc-only), or run
// against calibration. Designed to be safe to run from a laptop SSH'd into
// the mainnet node.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Reiers/sp-radar/internal/chaincrawl"
	"github.com/Reiers/sp-radar/internal/cluster"
	"github.com/Reiers/sp-radar/internal/foc"
	"github.com/Reiers/sp-radar/internal/geoip"
	"github.com/Reiers/sp-radar/internal/probe"
	"github.com/Reiers/sp-radar/internal/render"
	"github.com/Reiers/sp-radar/internal/scanner"
	"github.com/Reiers/sp-radar/internal/snapshot"
	"github.com/Reiers/sp-radar/internal/spscan"
	"github.com/urfave/cli/v2"
)

var version = "0.1.0-dev"

func main() {
	app := &cli.App{
		Name:    "filcensus",
		Usage:   "Filecoin Network Census — SPs, FoC providers, chain nodes",
		Version: version,
		Commands: []*cli.Command{
			censusCmd,
			enrichCmd,
			pushCmd,
			focCountCmd,
		},
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var censusCmd = &cli.Command{
	Name:  "census",
	Usage: "Run a full census snapshot (SPs + FoC + chain nodes + geoip)",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "api", Usage: "Lotus FULLNODE_API_INFO override", EnvVars: []string{"FULLNODE_API_INFO"}},
		&cli.StringFlag{Name: "network", Usage: "mainnet | calibration", Value: "mainnet"},
		&cli.StringFlag{Name: "out", Usage: "Output directory for snapshot JSON", Value: "snapshots"},
		&cli.IntFlag{Name: "max-sps", Usage: "Limit SP scan to N (0 = all)", Value: 0},
		&cli.StringFlag{Name: "sp-source", Usage: "Active-SP source: 'filfox' (fast, ~700 active miners) or 'chain-full' (slow, all 750k registered)", Value: "filfox"},
		&cli.IntFlag{Name: "concurrency", Usage: "libp2p concurrency", Value: 50},
		&cli.IntFlag{Name: "lotus-concurrency", Usage: "Lotus RPC concurrency", Value: 50},
		&cli.DurationFlag{Name: "timeout", Usage: "Per-peer libp2p timeout", Value: 10 * time.Second},
		&cli.BoolFlag{Name: "skip-sps", Usage: "Skip SP enumeration"},
		&cli.BoolFlag{Name: "skip-foc", Usage: "Skip FoC enumeration"},
		&cli.BoolFlag{Name: "skip-chain-nodes", Usage: "Skip chain-node crawl"},
		&cli.BoolFlag{Name: "with-geoip", Usage: "Inline GeoIP enrichment (slower; default off so live capture finishes fast)", Value: false},
		&cli.StringFlag{Name: "render", Usage: "If set, also render the static dashboard to this directory", Value: ""},
	},
	Action: runCensus,
}

func runCensus(c *cli.Context) error {
	ctx, cancel := signal.NotifyContext(c.Context, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	network := c.String("network")
	if network != "mainnet" && network != "calibration" {
		return fmt.Errorf("unknown --network %q (expected mainnet or calibration)", network)
	}
	api := c.String("api")
	if api == "" {
		return fmt.Errorf("--api or FULLNODE_API_INFO required")
	}

	outDir := c.String("out")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	rpc := scanner.NewLotusRPC(api)
	host, _ := os.Hostname()

	snap := &snapshot.Snapshot{
		Network:     network,
		GeneratedAt: time.Now().UTC(),
		GeneratedBy: fmt.Sprintf("filcensus@%s on %s", version, host),
		Run: snapshot.RunStats{
			StartedAt:  time.Now().UTC(),
			PhaseTimes: make(map[string]time.Duration),
		},
		Aggregates: snapshot.Aggregates{
			SPsBySoftware:  make(map[string]int),
			SPsByCountry:   make(map[string]int),
			SPsByASN:       make(map[string]int),
			ChainNodesBySW: make(map[string]int),
		},
	}

	// --- FoC phase (cheapest, run first so we have data even if SPs scan fails) ---
	if !c.Bool("skip-foc") {
		t0 := time.Now()
		if err := runFoCPhase(ctx, rpc, foc.Network(network), snap); err != nil {
			snap.Run.Errors = append(snap.Run.Errors, fmt.Sprintf("foc: %v", err))
			fmt.Fprintf(os.Stderr, "[foc] error: %v (continuing)\n", err)
		}
		snap.Run.PhaseTimes["foc"] = time.Since(t0)
	}

	// --- SP phase ---
	if !c.Bool("skip-sps") {
		t0 := time.Now()
		if err := runSPPhase(ctx, c, snap); err != nil {
			snap.Run.Errors = append(snap.Run.Errors, fmt.Sprintf("sps: %v", err))
			fmt.Fprintf(os.Stderr, "[sps] error: %v (continuing)\n", err)
		}
		snap.Run.PhaseTimes["sps"] = time.Since(t0)
	}

	// --- Chain-node crawl phase ---
	if !c.Bool("skip-chain-nodes") {
		t0 := time.Now()
		if err := runChainCrawlPhase(ctx, c, snap); err != nil {
			snap.Run.Errors = append(snap.Run.Errors, fmt.Sprintf("chain-nodes: %v", err))
			fmt.Fprintf(os.Stderr, "[chain-nodes] error: %v (continuing)\n", err)
		}
		snap.Run.PhaseTimes["chain-nodes"] = time.Since(t0)
	}

	// --- HTTP probes against FoC serviceURLs ---
	if !c.Bool("skip-foc") && len(snap.FoCNodes) > 0 {
		t0 := time.Now()
		probeFoCHTTP(ctx, snap, 16)
		snap.Run.PhaseTimes["foc-http"] = time.Since(t0)
	}

	// --- GeoIP enrichment (only if explicitly requested) ---
	if c.Bool("with-geoip") {
		t0 := time.Now()
		if err := runGeoIPPhase(ctx, snap); err != nil {
			snap.Run.Errors = append(snap.Run.Errors, fmt.Sprintf("geoip: %v", err))
			fmt.Fprintf(os.Stderr, "[geoip] error: %v (continuing)\n", err)
		}
		snap.Run.PhaseTimes["geoip"] = time.Since(t0)
	} else {
		fmt.Fprintln(os.Stderr, "[geoip] skipped (use 'filcensus enrich' or --with-geoip to enrich)")
	}

	computeOperators(snap)
	computeAggregates(snap)

	// --- Finish ---
	snap.Run.FinishedAt = time.Now().UTC()
	snap.Run.Duration = snap.Run.FinishedAt.Sub(snap.Run.StartedAt)

	out := filepath.Join(outDir, fmt.Sprintf("%s-%s.json", network, snap.GeneratedAt.Format("2006-01-02")))
	if err := snapshot.Write(out, snap); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	fmt.Printf("Wrote %s\n", out)
	fmt.Printf("  Miner IDs:     %d (%d real / %d ghost)\n",
		snap.Aggregates.SPsTotal, snap.Aggregates.SPsReal, snap.Aggregates.SPsGhost)
	fmt.Printf("  Operators:     %d (%.1fx dedup) — %d reachable\n",
		snap.Aggregates.OperatorsTotal, snap.Aggregates.DedupRatio, snap.Aggregates.OperatorsReachable)
	fmt.Printf("  Probed:        %d reachable / %d total miner IDs\n",
		snap.Aggregates.SPsReachable, snap.Aggregates.SPsTotal)
	fmt.Printf("  Chain nodes:   %d\n", snap.Aggregates.ChainNodesTotal)
	fmt.Printf("  FoC providers: %d (%d active, %d reachable)\n",
		snap.Aggregates.FoCNodesTotal, snap.Aggregates.FoCNodesActive, snap.Aggregates.FoCNodesReachable)
	fmt.Printf("  Duration:      %s\n", snap.Run.Duration)

	if renderDir := c.String("render"); renderDir != "" {
		if err := render.Render(snap, renderDir); err != nil {
			return fmt.Errorf("render: %w", err)
		}
		fmt.Printf("Rendered dashboard to %s/index.html\n", renderDir)
	}
	return nil
}

// runChainCrawlPhase walks NetPeers + NetAgentVersion to enumerate the
// directly-connected chain-node fleet.
func runChainCrawlPhase(ctx context.Context, c *cli.Context, snap *snapshot.Snapshot) error {
	fmt.Fprintln(os.Stderr, "[chain-nodes] crawling NetPeers...")
	recs, err := chaincrawl.Run(ctx, chaincrawl.Options{
		APIInfo:          c.String("api"),
		AgentConcurrency: 16,
		OnProgress: func(done, total int64) {
			fmt.Fprintf(os.Stderr, "[chain-nodes] %d/%d\n", done, total)
		},
	})
	if err != nil {
		return err
	}
	snap.ChainNodes = recs
	return nil
}

// runSPPhase enumerates and probes the SP fleet.
func runSPPhase(ctx context.Context, c *cli.Context, snap *snapshot.Snapshot) error {
	fmt.Fprintln(os.Stderr, "[sps] enumerating storage providers...")
	var lastPhase string
	records, err := spscan.Run(ctx, spscan.Options{
		APIInfo:          c.String("api"),
		Source:           spscan.Source(c.String("sp-source")),
		Concurrency:      c.Int("concurrency"),
		LotusConcurrency: c.Int("lotus-concurrency"),
		MaxProviders:     c.Int("max-sps"),
		Timeout:          c.Duration("timeout"),
		OnProgress: func(phase string, done, total int64) {
			if phase != lastPhase {
				lastPhase = phase
				fmt.Fprintf(os.Stderr, "[sps] phase=%s\n", phase)
			}
			fmt.Fprintf(os.Stderr, "[sps] %s %d/%d\n", phase, done, total)
		},
	})
	if err != nil {
		return err
	}
	snap.SPs = records
	return nil
}

// probeFoCHTTP runs an HTTP /pdp/ping probe against each FoC serviceURL,
// updating each row in place.
func probeFoCHTTP(ctx context.Context, snap *snapshot.Snapshot, concurrency int) {
	client := &http.Client{Timeout: 8 * time.Second}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range snap.FoCNodes {
		if snap.FoCNodes[i].ServiceURL == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			res := probe.PingFoCService(ctx, client, snap.FoCNodes[idx].ServiceURL)
			snap.FoCNodes[idx].HTTPReachable = res.Reachable
			snap.FoCNodes[idx].HTTPStatusCode = res.StatusCode
			snap.FoCNodes[idx].HTTPServerHeader = res.ServerHeader
			if res.Err != "" {
				snap.FoCNodes[idx].HTTPError = res.Err
			}
		}(i)
	}
	wg.Wait()
}

// runGeoIPPhase resolves serviceURL hostnames + SP IPs and looks them up via
// MaxMind GeoLite2 if MAXMIND_CITY_DB / MAXMIND_ASN_DB are configured.
func runGeoIPPhase(ctx context.Context, snap *snapshot.Snapshot) error {
	mm, err := geoip.NewMaxMindFromEnv()
	if err != nil {
		return err
	}
	if mm == nil {
		fmt.Fprintln(os.Stderr, "[geoip] MAXMIND_CITY_DB / MAXMIND_ASN_DB not set; skipping enrichment")
		return nil
	}
	defer mm.Close()
	cache := geoip.NewCache(mm)

	// Enrich SPs
	for i := range snap.SPs {
		for _, ip := range snap.SPs[i].IPs {
			r, err := cache.Lookup(ctx, ip)
			if err != nil || r == nil {
				continue
			}
			snap.SPs[i].GeoIP = append(snap.SPs[i].GeoIP, snapshot.GeoRow{
				IP:          r.IP,
				Country:     r.Country,
				CountryCode: r.CountryCode,
				Region:      r.Region,
				City:        r.City,
				ASN:         r.ASN,
				ASNOrg:      r.ASNOrg,
			})
		}
	}

	// Enrich chain nodes
	for i := range snap.ChainNodes {
		for _, ip := range snap.ChainNodes[i].IPs {
			r, err := cache.Lookup(ctx, ip)
			if err != nil || r == nil {
				continue
			}
			snap.ChainNodes[i].GeoIP = append(snap.ChainNodes[i].GeoIP, snapshot.GeoRow{
				IP:          r.IP,
				Country:     r.Country,
				CountryCode: r.CountryCode,
				Region:      r.Region,
				City:        r.City,
				ASN:         r.ASN,
				ASNOrg:      r.ASNOrg,
			})
		}
	}

	// Enrich FoC nodes by resolving serviceURL hostname → IPs first.
	for i := range snap.FoCNodes {
		host := probe.HostnameOf(snap.FoCNodes[i].ServiceURL)
		if host == "" {
			continue
		}
		ips := resolveHost(ctx, host)
		snap.FoCNodes[i].ResolvedIPs = ips
		for _, ip := range ips {
			r, err := cache.Lookup(ctx, ip)
			if err != nil || r == nil {
				continue
			}
			snap.FoCNodes[i].GeoIP = append(snap.FoCNodes[i].GeoIP, snapshot.GeoRow{
				IP:          r.IP,
				Country:     r.Country,
				CountryCode: r.CountryCode,
				Region:      r.Region,
				City:        r.City,
				ASN:         r.ASN,
				ASNOrg:      r.ASNOrg,
			})
		}
		snap.FoCNodes[i].LocationMatch = compareLocations(snap.FoCNodes[i].DeclaredLocation, snap.FoCNodes[i].GeoIP)
	}

	return nil
}

func resolveHost(ctx context.Context, host string) []string {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res := &net.Resolver{}
	addrs, err := res.LookupIPAddr(rctx, host)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range addrs {
		s := a.IP.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// compareLocations does a coarse declared-vs-resolved location match.
// declared looks like "C=US;ST=California;L=Chino". geo provides ISO codes.
// We say "match" if any resolved CountryCode matches the declared C= field,
// "mismatch" if at least one resolved code disagrees, else "unknown".
func compareLocations(declared string, geo []snapshot.GeoRow) string {
	if declared == "" || len(geo) == 0 {
		return "unknown"
	}
	var declaredCC string
	for _, kv := range strings.Split(declared, ";") {
		kv = strings.TrimSpace(kv)
		if strings.HasPrefix(strings.ToUpper(kv), "C=") {
			declaredCC = strings.ToUpper(strings.TrimPrefix(kv, "C="))
			declaredCC = strings.ToUpper(strings.TrimPrefix(declaredCC, "c="))
			break
		}
	}
	if declaredCC == "" {
		return "unknown"
	}
	match := false
	mismatch := false
	for _, g := range geo {
		if g.CountryCode == "" {
			continue
		}
		if strings.EqualFold(g.CountryCode, declaredCC) {
			match = true
		} else {
			mismatch = true
		}
	}
	switch {
	case match && !mismatch:
		return "match"
	case mismatch && !match:
		return "mismatch"
	case match && mismatch:
		return "partial"
	}
	return "unknown"
}

// computeAggregates fills snap.Aggregates from the per-record collections.
// computeOperators runs union-find clustering on the SP records and fills
// snap.Operators. Mirrors the 2026-Q1 SP census report's grouping logic
// (shared owner / worker / control / beneficiary / IP, with a CDN cap).
//
// We only cluster SPs with non-zero raw byte power. Zero-power miner IDs are
// chain ghosts (registered but storing nothing) and would each become their
// own singleton cluster, inflating the operator count without representing
// real entities. They stay in snap.SPs so the per-miner detail is preserved,
// but the dashboard's headline ("N operators run the network") only counts
// real actors.
func computeOperators(snap *snapshot.Snapshot) {
	ids := make([]cluster.Identity, 0, len(snap.SPs))
	reachableByID := make(map[string]bool, len(snap.SPs))
	ghosts := 0
	for _, sp := range snap.SPs {
		reachableByID[sp.MinerID] = sp.Reachable
		if !hasNonZeroPower(sp.RawBytePower) {
			ghosts++
			continue
		}
		controls := append([]string(nil), sp.ControlAddrs...)
		ids = append(ids, cluster.Identity{
			MinerID:         sp.MinerID,
			Owner:           sp.OwnerAddr,
			Worker:          sp.WorkerAddr,
			Control:         controls,
			Beneficiary:     sp.BeneficiaryAddr,
			IPs:             append([]string(nil), sp.IPs...),
			RawBytePower:    sp.RawBytePower,
			QualityAdjPower: sp.QualityAdjPower,
		})
	}
	if ghosts > 0 {
		fmt.Fprintf(os.Stderr, "[cluster] excluded %d zero-power miner IDs from operator clustering\n", ghosts)
	}
	clusters := cluster.Build(ids)
	snap.Operators = make([]snapshot.Operator, 0, len(clusters))
	for _, c := range clusters {
		op := snapshot.Operator{
			Representative:  c.Representative,
			Members:         c.Members,
			Owners:          c.Owners,
			Workers:         c.Workers,
			Beneficiaries:   c.Beneficiaries,
			IPs:             c.IPs,
			RawBytePower:    c.RawBytePower,
			QualityAdjPower: c.QualityAdjPower,
		}
		for _, m := range c.Members {
			if reachableByID[m] {
				op.ReachableMembers++
			} else {
				op.UnreachableMembers++
			}
		}
		snap.Operators = append(snap.Operators, op)
	}
}

// hasNonZeroPower returns true if the big-int decimal string parses to > 0.
// Empty / unparseable / "0" all return false (treated as ghost).
func hasNonZeroPower(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return false
	}
	for _, c := range s {
		if c >= '1' && c <= '9' {
			return true
		}
	}
	return false
}

func computeAggregates(snap *snapshot.Snapshot) {
	ag := &snap.Aggregates
	ag.SPsTotal = len(snap.SPs)
	for _, sp := range snap.SPs {
		if hasNonZeroPower(sp.RawBytePower) {
			ag.SPsReal++
		} else {
			ag.SPsGhost++
		}
		if sp.Reachable {
			ag.SPsReachable++
		}
		if sp.Software != "" {
			ag.SPsBySoftware[sp.Software]++
		} else {
			ag.SPsBySoftware["unknown"]++
		}
		for _, g := range sp.GeoIP {
			if g.CountryCode != "" {
				ag.SPsByCountry[g.CountryCode]++
			}
			if g.ASN != 0 {
				ag.SPsByASN[fmt.Sprintf("AS%d", g.ASN)]++
			}
		}
	}
	ag.ChainNodesTotal = len(snap.ChainNodes)
	for _, cn := range snap.ChainNodes {
		if cn.Software != "" {
			ag.ChainNodesBySW[cn.Software]++
		}
	}
	ag.FoCNodesTotal = len(snap.FoCNodes)
	for _, f := range snap.FoCNodes {
		if f.Active {
			ag.FoCNodesActive++
		}
		if f.HTTPReachable {
			ag.FoCNodesReachable++
		}
	}
	ag.OperatorsTotal = len(snap.Operators)
	for _, op := range snap.Operators {
		if op.ReachableMembers > 0 {
			ag.OperatorsReachable++
		}
	}
	if ag.OperatorsTotal > 0 {
		// Dedup ratio is real SPs (non-ghost) / operators — the headline
		// number that says how aggressive the consolidation is.
		ag.DedupRatio = float64(ag.SPsReal) / float64(ag.OperatorsTotal)
	}
	// Sorted variants are not on the struct (the renderer can sort the maps)
	_ = sort.Strings
}

func runFoCPhase(ctx context.Context, rpc *scanner.LotusRPC, network foc.Network, snap *snapshot.Snapshot) error {
	fmt.Fprintf(os.Stderr, "[foc] enumerating registry on %s (%s)...\n", network, foc.RegistryAddress(network))

	count, err := foc.GetActiveProviderCount(ctx, rpc, network)
	if err != nil {
		return fmt.Errorf("activeProviderCount: %w", err)
	}
	totalCount, _ := foc.GetProviderCount(ctx, rpc, network)
	fmt.Fprintf(os.Stderr, "[foc] active=%s total_ever_registered=%s\n", count, totalCount)

	ids, err := foc.EnumerateActiveProviderIDs(ctx, rpc, network, 100)
	if err != nil {
		return fmt.Errorf("enumerate: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[foc] enumerated %d active provider IDs\n", len(ids))

	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := foc.GetProvider(ctx, rpc, network, id)
		if err != nil {
			snap.Run.Errors = append(snap.Run.Errors, fmt.Sprintf("foc:getProvider(%s): %v", id, err))
			fmt.Fprintf(os.Stderr, "[foc] %d/%d id=%s ERR: %v\n", i+1, len(ids), id, err)
			continue
		}
		row := snapshot.FoCNodeRecord{
			ProviderID:         p.ID.String(),
			ServiceProviderHex: p.ServiceProviderHex,
			PayeeHex:           p.PayeeHex,
			Name:               p.Name,
			Description:        p.Description,
			Active:             p.Active,
			ProductType:        "PDP",
			ServiceURL:         p.PDP.ServiceURL,
			DeclaredLocation:   p.PDP.Location,
			IPNIPeerID:         p.PDP.IPNIPeerID,
			IPNISupportsPiece:  p.PDP.IPNISupportsPiece,
			IPNISupportsIPFS:   p.PDP.IPNISupportsIPFS,
		}
		if p.PDP.MinPieceSizeBytes != nil {
			row.MinPieceSizeBytes = p.PDP.MinPieceSizeBytes.String()
		}
		if p.PDP.MaxPieceSizeBytes != nil {
			row.MaxPieceSizeBytes = p.PDP.MaxPieceSizeBytes.String()
		}
		if p.PDP.StoragePricePerTibPerDay != nil {
			row.StoragePricePerTibPerDay = p.PDP.StoragePricePerTibPerDay.String()
		}
		if p.PDP.MinProvingPeriodEpochs != nil {
			row.MinProvingPeriodEpochs = p.PDP.MinProvingPeriodEpochs.String()
		}
		row.PaymentTokenAddress = p.PDP.PaymentTokenAddress

		snap.FoCNodes = append(snap.FoCNodes, row)
		// Don't increment Aggregates here — computeAggregates() handles
		// the final pass after all phases populate snap.FoCNodes.

		if i%10 == 0 || i == len(ids)-1 {
			fmt.Fprintf(os.Stderr, "[foc] %d/%d %s (%s)\n", i+1, len(ids), p.Name, p.PDP.ServiceURL)
		}
	}
	return nil
}

// enrichCmd applies GeoIP enrichment to an existing snapshot file in-place.
// Decoupled from `census` so the live data capture stays fast and we don't
// risk losing the data fetch if MaxMind has an issue.
var enrichCmd = &cli.Command{
	Name:      "enrich",
	Usage:     "Apply GeoIP enrichment to an existing snapshot file in-place",
	ArgsUsage: "<snapshot.json>",
	Flags: []cli.Flag{
		&cli.BoolFlag{Name: "recompute-aggregates", Usage: "Recompute snap.Aggregates after enrichment", Value: true},
	},
	Action: func(c *cli.Context) error {
		if c.NArg() != 1 {
			return fmt.Errorf("usage: filcensus enrich <snapshot.json>")
		}
		path := c.Args().First()
		snap, err := snapshot.Read(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		t0 := time.Now()
		if err := runGeoIPPhase(c.Context, snap); err != nil {
			return fmt.Errorf("geoip: %w", err)
		}
		snap.Run.PhaseTimes["geoip"] = time.Since(t0)
		if c.Bool("recompute-aggregates") {
			// Reset and recompute. Existing aggregates may include stale empty maps.
			snap.Aggregates = snapshot.Aggregates{
				SPsBySoftware:  make(map[string]int),
				SPsByCountry:   make(map[string]int),
				SPsByASN:       make(map[string]int),
				ChainNodesBySW: make(map[string]int),
			}
			computeOperators(snap)
			computeAggregates(snap)
		}
		if err := snapshot.Write(path, snap); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		fmt.Printf("Enriched %s in %s\n", path, snap.Run.PhaseTimes["geoip"])
		return nil
	},
}

// focCountCmd is a quick smoke test that just prints provider counts. Useful
// for verifying the API and the registry contract are reachable before
// running a full census.
var focCountCmd = &cli.Command{
	Name:  "foc-count",
	Usage: "Print FoC provider counts for the given network (smoke test)",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "api", Usage: "Lotus FULLNODE_API_INFO override", EnvVars: []string{"FULLNODE_API_INFO"}},
		&cli.StringFlag{Name: "network", Usage: "mainnet | calibration", Value: "mainnet"},
	},
	Action: func(c *cli.Context) error {
		api := c.String("api")
		if api == "" {
			return fmt.Errorf("--api or FULLNODE_API_INFO required")
		}
		network := foc.Network(c.String("network"))
		if foc.RegistryAddress(network) == "" {
			return fmt.Errorf("unknown network %q", network)
		}
		rpc := scanner.NewLotusRPC(api)
		ctx := c.Context
		active, err := foc.GetActiveProviderCount(ctx, rpc, network)
		if err != nil {
			return err
		}
		total, err := foc.GetProviderCount(ctx, rpc, network)
		if err != nil {
			return err
		}
		fmt.Printf("network: %s\n", network)
		fmt.Printf("registry: %s\n", foc.RegistryAddress(network))
		fmt.Printf("active providers:    %s\n", active)
		fmt.Printf("total ever registered: %s\n", total)
		return nil
	},
}
