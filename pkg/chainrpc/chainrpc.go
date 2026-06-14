// Package chainrpc is the public, stable re-export of the Lotus-compatible
// JSON-RPC client implemented in internal/scanner. It lets other modules
// (e.g. github.com/Reiers/filmatch) talk to a Lantern daemon's Lotus RPC
// surface (StateMinerPower, eth_call, ...) without importing an internal
// package.
package chainrpc

import (
	"github.com/Reiers/sp-radar/internal/scanner"
)

// LotusRPC is a Lotus-compatible JSON-RPC client. It satisfies foc.EthCaller.
type LotusRPC = scanner.LotusRPC

// MinerPowerResult is the StateMinerPower return shape.
type MinerPowerResult = scanner.MinerPowerResult

// PowerClaim holds raw + quality-adjusted power as big-int strings.
type PowerClaim = scanner.PowerClaim

// NewLotusRPC builds a client from an api-info string
// ("<token>:<multiaddr>" or a bare http URL).
func NewLotusRPC(apiInfo string) *LotusRPC { return scanner.NewLotusRPC(apiInfo) }
