package spscan

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"time"
)

// FilfoxAPI is the public Filfox endpoint we read /miner/list/power from.
const FilfoxAPI = "https://filfox.info/api/v1"

// filfoxPowerPage mirrors the Filfox response shape for /miner/list/power.
type filfoxPowerPage struct {
	TotalCount int             `json:"totalCount"`
	Miners     []filfoxMinerEntry `json:"miners"`
}

type filfoxMinerEntry struct {
	Address              string `json:"address"`
	RawBytePower         string `json:"rawBytePower"`
	QualityAdjPower      string `json:"qualityAdjPower"`
	RawBytePowerDelta    string `json:"rawBytePowerDelta"`
	QualityAdjPowerDelta string `json:"qualityAdjPowerDelta"`
}

// FilfoxDeltaByAddr is the per-miner power delta from the most recent
// Filfox measurement period (typically 24h or 1 epoch-aligned window).
// Negative = losing power; positive = gaining. Populated as a side-effect
// of fetchFilfoxActiveMiners and exposed for the runner to attach to
// SPRecord.
var FilfoxDeltaByAddr map[string]filfoxMinerEntry

// fetchFilfoxActiveMiners returns the full set of currently-active miners
// from Filfox, with their power already attached. This avoids doing 750k
// StateMinerPower lookups against the chain just to filter to ~700 actives.
//
// As a side-effect, populates FilfoxDeltaByAddr so the runner can attach
// rawBytePowerDelta / qualityAdjPowerDelta to each SP record without
// re-fetching.
//
// Public API. Be polite: 0.3s delay between pages.
func fetchFilfoxActiveMiners(ctx context.Context, onProgress func(string, int64, int64)) ([]qualifiedSP, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	out := []qualifiedSP{}
	const pageSize = 100
	page := 0
	total := 0

	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		u := fmt.Sprintf("%s/miner/list/power?pageSize=%d&page=%d", FilfoxAPI, pageSize, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return out, err
		}
		req.Header.Set("User-Agent", "filcensus/0.1 (+https://filcensus.reiers.io)")
		resp, err := client.Do(req)
		if err != nil {
			return out, fmt.Errorf("filfox page %d: %w", page, err)
		}
		body := resp.Body
		var data filfoxPowerPage
		dec := json.NewDecoder(body)
		dec.UseNumber()
		if err := dec.Decode(&data); err != nil {
			body.Close()
			return out, fmt.Errorf("filfox decode page %d: %w", page, err)
		}
		body.Close()
		if total == 0 {
			total = data.TotalCount
		}
		if len(data.Miners) == 0 {
			break
		}
		for _, m := range data.Miners {
			raw, _ := new(big.Int).SetString(m.RawBytePower, 10)
			qual, _ := new(big.Int).SetString(m.QualityAdjPower, 10)
			if raw == nil {
				raw = new(big.Int)
			}
			if qual == nil {
				qual = new(big.Int)
			}
			out = append(out, qualifiedSP{addr: m.Address, rawPower: raw, qualPower: qual})
			if FilfoxDeltaByAddr == nil {
				FilfoxDeltaByAddr = make(map[string]filfoxMinerEntry)
			}
			FilfoxDeltaByAddr[m.Address] = m
		}
		if onProgress != nil {
			onProgress("filfox-list", int64(len(out)), int64(total))
		}
		if len(out) >= total {
			break
		}
		page++
		// Be polite — Filfox rate-limits aggressive crawls.
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return out, nil
}

// hostnameForFilfox is here so we can swap to a self-hosted Filfox or proxy
// later by overriding this var in tests / wiring.
var hostnameForFilfox = func(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}

var _ = hostnameForFilfox // currently unused; reserved for an upcoming proxy/override hook
