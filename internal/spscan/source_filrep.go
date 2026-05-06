package spscan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// FilrepAPI is the public Filrep endpoint we read miner reputation metadata from.
const FilrepAPI = "https://api.filrep.io/api/v1/miners"

// IMPORTANT: Filrep is enrichment ONLY, not a liveness source.
//
// Filrep keeps miners with active power numbers in its table long after they
// go offline (they linger for weeks with status=true even when they are
// unreachable in practice). So we never use Filrep to decide which SPs to
// scan, only to attach reachability / uptime / country labels on top of the
// canonical Filfox set for cross-validation against our own libp2p probe.
//
// See: https://api.filrep.io/api/v1/miners?limit=1 — `reachability` field
// flips between "reachable" and "unreachable" but `status:true` and rawPower
// stay populated past the dropout, which is why a Filrep-only set of "active"
// miners over-counts dead nodes by an order of magnitude (~6,400 vs the real
// ~726).

// filrepResponse mirrors the Filrep API response.
type filrepResponse struct {
	Pagination struct {
		Total int `json:"total"`
		Limit int `json:"limit"`
	} `json:"pagination"`
	Miners []FilrepMiner `json:"miners"`
}

// FilrepMiner is one row from Filrep, exposed so the runner can attach the
// metadata fields to SPRecord.
//
// Filrep's schema is loose. Several fields are typed inconsistently across
// rows (number vs string), so we keep type-flexible fields as `any` and
// project them at the use site.
type FilrepMiner struct {
	Address       string `json:"address"`
	Status        bool   `json:"status"`
	Reachability  string `json:"reachability"`  // "reachable" | "unreachable" | "unknown"
	UptimeAverage any    `json:"uptimeAverage"` // float or null
	IsoCode       string `json:"isoCode"`
	Region        string `json:"region"`
	Score         any    `json:"score"` // sometimes string, sometimes number
}

// fetchFilrepEnrichment fetches Filrep's miner table and returns it indexed
// by f0... address. Best-effort: on any failure we log to stderr and return
// an empty map so the caller can continue without enrichment.
func fetchFilrepEnrichment(ctx context.Context, onProgress func(string, int64, int64)) map[string]FilrepMiner {
	out := map[string]FilrepMiner{}
	client := &http.Client{Timeout: 30 * time.Second}
	url := fmt.Sprintf("%s?limit=10000", FilrepAPI)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[filrep-meta] request build: %v (skipping enrichment)\n", err)
		return out
	}
	req.Header.Set("User-Agent", "filcensus/0.1 (+https://filcensus.reiers.io)")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[filrep-meta] %v (skipping enrichment)\n", err)
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "[filrep-meta] HTTP %d (skipping enrichment)\n", resp.StatusCode)
		return out
	}
	var body filrepResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		fmt.Fprintf(os.Stderr, "[filrep-meta] decode: %v (skipping enrichment)\n", err)
		return out
	}
	for _, m := range body.Miners {
		out[m.Address] = m
	}
	if onProgress != nil {
		onProgress("filrep-meta", int64(len(body.Miners)), int64(body.Pagination.Total))
	}
	return out
}
