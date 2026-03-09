package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/Reiers/sp-radar/internal/scanner"
	"github.com/urfave/cli/v2"
)

var scanCmd = &cli.Command{
	Name:  "scan",
	Usage: "Scan the Filecoin network and detect SP software",
	Flags: []cli.Flag{
		&cli.IntFlag{Name: "concurrency", Aliases: []string{"c"}, Value: 50, Usage: "Concurrent libp2p SP queries"},
		&cli.IntFlag{Name: "lotus-concurrency", Value: 50, Usage: "Concurrent Lotus API calls"},
		&cli.IntFlag{Name: "max-providers", Value: 0, Usage: "Limit providers (0 = all)"},
		&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Value: "text", Usage: "Format: text or json"},
		&cli.StringFlag{Name: "json-file", Usage: "Write JSON results to file"},
		&cli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Usage: "Show per-SP details"},
		&cli.DurationFlag{Name: "timeout", Value: 10 * time.Second, Usage: "Per-SP connection timeout"},
		&cli.StringFlag{Name: "api", EnvVars: []string{"FULLNODE_API_INFO"}, Usage: "Lotus gateway endpoint"},
	},
	Action: func(cctx *cli.Context) error {
		api := cctx.String("api")
		if api == "" {
			return fmt.Errorf("set FULLNODE_API_INFO or use --api")
		}

		ctx, cancel := context.WithCancel(cctx.Context)
		defer cancel()
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		go func() { <-ch; cancel() }()

		r, err := scanner.Scan(ctx, scanner.ScanOptions{
			APIInfo:          api,
			Concurrency:      cctx.Int("concurrency"),
			LotusConcurrency: cctx.Int("lotus-concurrency"),
			MaxProviders:     cctx.Int("max-providers"),
			Verbose:          cctx.Bool("verbose"),
			Timeout:          cctx.Duration("timeout"),
		})
		if err != nil {
			return err
		}

		if cctx.String("output") == "json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(r)
		}
		printReport(r)

		if p := cctx.String("json-file"); p != "" {
			f, err := os.Create(p)
			if err != nil {
				return err
			}
			defer f.Close()
			json.NewEncoder(f).Encode(r)
			fmt.Fprintf(os.Stderr, "Saved to %s\n", p)
		}
		return nil
	},
}

func printReport(r *scanner.ScanResult) {
	fmt.Println()
	fmt.Println("SP Radar - Filecoin Storage Provider Network Scan")
	fmt.Println("==================================================")
	fmt.Printf("Scanned: %s | SPs on chain: %s | Min power: %s\n\n",
		r.Timestamp.Format(time.RFC3339), comma(r.TotalSPsOnChain), comma(r.TotalSPsWithMinPower))

	t := r.TotalSPsScanned
	if t == 0 {
		t = 1
	}
	fmt.Println("Software Distribution:")
	for _, e := range []struct {
		n string
		d scanner.SoftwareStats
	}{
		{"Curio", r.Software.Curio}, {"Boost", r.Software.Boost},
		{"Venus", r.Software.Venus}, {"Markets", r.Software.Markets},
		{"Unknown", r.Software.Unknown},
	} {
		fmt.Printf("  %-10s %4d SPs  %10.2f PiB QAP  (%5.1f%%)\n",
			e.n, e.d.Count, e.d.QualityAdjPowerPiB, float64(e.d.Count)/float64(t)*100)
	}
	fmt.Printf("\nIndexer nodes: %d\n", r.IndexerNodes)
	if len(r.AgentVersions) > 0 {
		fmt.Println("\nAgent Versions:")
		for _, a := range r.AgentVersionsSorted() {
			fmt.Printf("  %s: %d\n", a.Name, a.Count)
		}
	}
	fmt.Println()
}

func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, byte(c))
	}
	return string(b)
}
