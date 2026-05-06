// Package networktruth queries the Filecoin chain for headline aggregate
// numbers: raw + QA power, total deals, locked collateral, allocator counts.
//
// All numbers come from a single-shot StateReadState on the system actors:
//
//   f04  StoragePower    — TotalRawBytePower, TotalQualityAdjPower, MinerCount, MinerAboveMinPowerCount, TotalPledgeCollateral
//   f05  StorageMarket   — NextID (deal count), TotalProviderLockedCollateral, Balance
//   f06  VerifiedRegistry — NextAllocationId (DataCap allocations issued)
//
// One run = ~3 cheap RPC calls. Cost is negligible compared to the SP probe
// phase, but the resulting numbers are the most interesting ones on the
// whole dashboard.
package networktruth

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/Reiers/sp-radar/internal/scanner"
)

// Result is a single chain-truth snapshot of the network's aggregate state.
type Result struct {
	HeadEpoch int64

	// f04 (StoragePower) fields
	TotalRawBytePower      *big.Int // bytes
	TotalQualityAdjPower   *big.Int // bytes (QA-weighted)
	TotalPledgeCollateral  *big.Int // attoFIL
	MinerCount             int64    // total registered
	MinerAboveMinPowerCount int64   // active set per chain

	// f05 (StorageMarket) fields
	StorageMarketBalance     *big.Int // attoFIL: escrow + collateral combined
	StorageMarketLocked      *big.Int // attoFIL: provider locked collateral
	StorageMarketLastCron    int64    // last epoch f05 cron ran
	NextDealID               int64    // total deals ever created (high-watermark)

	// f06 (VerifiedRegistry) fields
	NextAllocationID  int64    // total DataCap allocations ever issued
	VerifiedRootKey   string   // governance root multisig (currently f080)
}

// Fetch reads the three system actors and returns a populated Result.
// On any per-actor RPC failure, the returned Result has the unaffected fields
// populated and partial-failure errors are joined into the returned error.
func Fetch(ctx context.Context, rpc *scanner.LotusRPC) (*Result, error) {
	res := &Result{
		TotalRawBytePower:     new(big.Int),
		TotalQualityAdjPower:  new(big.Int),
		TotalPledgeCollateral: new(big.Int),
		StorageMarketBalance:  new(big.Int),
		StorageMarketLocked:   new(big.Int),
	}
	var firstErr error

	// Chain head for context.
	head, err := chainHead(ctx, rpc)
	if err != nil {
		firstErr = fmt.Errorf("chain head: %w", err)
	} else {
		res.HeadEpoch = head
	}

	// f04 — StoragePower
	if state, balance, err := readActorState(ctx, rpc, "f04"); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("f04: %w", err)
		}
	} else {
		_ = balance
		setBigFromMap(state, "TotalRawBytePower", res.TotalRawBytePower)
		setBigFromMap(state, "TotalQualityAdjPower", res.TotalQualityAdjPower)
		setBigFromMap(state, "TotalPledgeCollateral", res.TotalPledgeCollateral)
		res.MinerCount = intFromMap(state, "MinerCount")
		res.MinerAboveMinPowerCount = intFromMap(state, "MinerAboveMinPowerCount")
	}

	// f05 — StorageMarket
	if state, balance, err := readActorState(ctx, rpc, "f05"); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("f05: %w", err)
		}
	} else {
		res.StorageMarketBalance = balance
		setBigFromMap(state, "TotalProviderLockedCollateral", res.StorageMarketLocked)
		res.NextDealID = intFromMap(state, "NextID")
		res.StorageMarketLastCron = intFromMap(state, "LastCron")
	}

	// f06 — VerifiedRegistry
	if state, _, err := readActorState(ctx, rpc, "f06"); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("f06: %w", err)
		}
	} else {
		res.NextAllocationID = intFromMap(state, "NextAllocationId")
		if rk, ok := state["RootKey"].(string); ok {
			res.VerifiedRootKey = rk
		}
	}

	return res, firstErr
}

// chainHead returns the current head epoch.
func chainHead(ctx context.Context, rpc *scanner.LotusRPC) (int64, error) {
	var raw struct {
		Height int64 `json:"Height"`
	}
	if err := rpcCall(ctx, rpc, "ChainHead", []interface{}{}, &raw); err != nil {
		return 0, err
	}
	return raw.Height, nil
}

// readActorState calls Filecoin.StateReadState and returns the State map +
// actor balance. Balance is parsed as *big.Int from the attoFIL string.
func readActorState(ctx context.Context, rpc *scanner.LotusRPC, addr string) (map[string]interface{}, *big.Int, error) {
	var raw struct {
		Balance string                 `json:"Balance"`
		State   map[string]interface{} `json:"State"`
	}
	if err := rpcCall(ctx, rpc, "StateReadState", []interface{}{addr, nil}, &raw); err != nil {
		return nil, nil, err
	}
	balance := new(big.Int)
	if raw.Balance != "" {
		balance.SetString(raw.Balance, 10)
	}
	return raw.State, balance, nil
}

// rpcCall is a thin reflection-free wrapper that goes through the existing
// LotusRPC client by serialising/deserialising via json. We don't expose
// private rpc methods on scanner; instead we abstract over the public RPC
// surface which only exposes specific typed methods.
//
// Implementation detail: we add a tiny generic call helper to scanner.LotusRPC
// in the same commit so we don't have to duplicate the request plumbing.
func rpcCall(ctx context.Context, rpc *scanner.LotusRPC, method string, params []interface{}, out interface{}) error {
	raw, err := rpc.RawCall(ctx, method, params)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// setBigFromMap pulls a string field from the actor state and sets dst to its
// big.Int value. No-op if the key is absent or the value isn't a numeric string.
func setBigFromMap(m map[string]interface{}, key string, dst *big.Int) {
	if m == nil {
		return
	}
	v, ok := m[key]
	if !ok {
		return
	}
	s, ok := v.(string)
	if !ok {
		return
	}
	x, ok := new(big.Int).SetString(s, 10)
	if ok {
		dst.Set(x)
	}
}

// intFromMap pulls a numeric field (string OR float64 — JSON ambiguity) and
// returns it as int64. Zero on absence/parse failure.
func intFromMap(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		// Some fields come as decimal strings (NextID, NextAllocationId).
		n, ok := new(big.Int).SetString(x, 10)
		if !ok {
			return 0
		}
		return n.Int64()
	}
	return 0
}

// AsByteCounts returns raw and QA power as PiB float64 for display.
// 1 PiB = 1<<50 bytes.
func (r *Result) RawPiB() float64 {
	if r.TotalRawBytePower == nil {
		return 0
	}
	f := new(big.Float).SetInt(r.TotalRawBytePower)
	pib, _ := new(big.Float).Quo(f, big.NewFloat(1<<50)).Float64()
	return pib
}
func (r *Result) QAPiB() float64 {
	if r.TotalQualityAdjPower == nil {
		return 0
	}
	f := new(big.Float).SetInt(r.TotalQualityAdjPower)
	pib, _ := new(big.Float).Quo(f, big.NewFloat(1<<50)).Float64()
	return pib
}

// VerifiedRawPiBEstimate solves the QA = raw_committed + 9*raw_verified
// equation to estimate how many raw bytes are verified-deal data vs CC.
// Conservative: assumes pure 10x multiplier (current post-FIP cryptoeconomics).
func (r *Result) VerifiedRawPiBEstimate() float64 {
	if r.TotalRawBytePower == nil || r.TotalQualityAdjPower == nil {
		return 0
	}
	// raw_verified ≈ (QA - raw_total) / 9
	delta := new(big.Int).Sub(r.TotalQualityAdjPower, r.TotalRawBytePower)
	ver := new(big.Int).Quo(delta, big.NewInt(9))
	if ver.Sign() < 0 {
		return 0
	}
	f := new(big.Float).SetInt(ver)
	pib, _ := new(big.Float).Quo(f, big.NewFloat(1<<50)).Float64()
	return pib
}

// PledgeFIL returns total pledge collateral in whole FIL.
func (r *Result) PledgeFIL() float64 {
	if r.TotalPledgeCollateral == nil {
		return 0
	}
	f := new(big.Float).SetInt(r.TotalPledgeCollateral)
	fil, _ := new(big.Float).Quo(f, big.NewFloat(1e18)).Float64()
	return fil
}

// MarketBalanceFIL returns the f05 actor's total balance in whole FIL.
func (r *Result) MarketBalanceFIL() float64 {
	if r.StorageMarketBalance == nil {
		return 0
	}
	f := new(big.Float).SetInt(r.StorageMarketBalance)
	fil, _ := new(big.Float).Quo(f, big.NewFloat(1e18)).Float64()
	return fil
}

// MarketLockedFIL returns provider locked collateral in whole FIL.
func (r *Result) MarketLockedFIL() float64 {
	if r.StorageMarketLocked == nil {
		return 0
	}
	f := new(big.Float).SetInt(r.StorageMarketLocked)
	fil, _ := new(big.Float).Quo(f, big.NewFloat(1e18)).Float64()
	return fil
}
