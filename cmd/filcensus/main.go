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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Reiers/sp-radar/internal/foc"
	"github.com/Reiers/sp-radar/internal/scanner"
	"github.com/Reiers/sp-radar/internal/snapshot"
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
		&cli.IntFlag{Name: "concurrency", Usage: "libp2p concurrency", Value: 50},
		&cli.IntFlag{Name: "lotus-concurrency", Usage: "Lotus RPC concurrency", Value: 50},
		&cli.DurationFlag{Name: "timeout", Usage: "Per-peer libp2p timeout", Value: 10 * time.Second},
		&cli.BoolFlag{Name: "skip-sps", Usage: "Skip SP enumeration"},
		&cli.BoolFlag{Name: "skip-foc", Usage: "Skip FoC enumeration"},
		&cli.BoolFlag{Name: "skip-chain-nodes", Usage: "Skip chain-node crawl"},
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

	// SP and chain-node phases will be wired in follow-up commits as the
	// runner stabilises. The scaffolding is here so the snapshot file
	// shape is correct even when those phases are skipped.
	if !c.Bool("skip-sps") {
		fmt.Fprintln(os.Stderr, "[sps] not yet wired in cmd/filcensus (legacy cmd/sp-radar still works)")
	}
	if !c.Bool("skip-chain-nodes") {
		fmt.Fprintln(os.Stderr, "[chain-nodes] not yet wired in cmd/filcensus")
	}

	// --- Finish ---
	snap.Run.FinishedAt = time.Now().UTC()
	snap.Run.Duration = snap.Run.FinishedAt.Sub(snap.Run.StartedAt)

	out := filepath.Join(outDir, fmt.Sprintf("%s-%s.json", network, snap.GeneratedAt.Format("2006-01-02")))
	if err := snapshot.Write(out, snap); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	fmt.Printf("Wrote %s\n", out)
	fmt.Printf("  FoC providers: %d (%d active, %d reachable)\n",
		snap.Aggregates.FoCNodesTotal, snap.Aggregates.FoCNodesActive, snap.Aggregates.FoCNodesReachable)
	fmt.Printf("  Duration: %s\n", snap.Run.Duration)
	return nil
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
		snap.Aggregates.FoCNodesTotal++
		if p.Active {
			snap.Aggregates.FoCNodesActive++
		}
		if i%10 == 0 || i == len(ids)-1 {
			fmt.Fprintf(os.Stderr, "[foc] %d/%d %s (%s)\n", i+1, len(ids), p.Name, p.PDP.ServiceURL)
		}
	}
	return nil
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
