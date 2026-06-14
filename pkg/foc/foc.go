// Package foc is the public, stable re-export of the FoC
// ServiceProviderRegistry / PDP-offering decoders implemented in
// internal/foc. It exists so other modules (e.g. github.com/Reiers/filmatch)
// can consume the chain-truth FoC supply data without reaching into an
// internal package.
//
// Pure-Go, zero Glif: all calls go through an EthCaller (a Lantern daemon's
// eth_call surface).
package foc

import (
	"context"
	"math/big"

	internalfoc "github.com/Reiers/sp-radar/internal/foc"
)

// Network selects mainnet vs calibration registry addresses.
type Network = internalfoc.Network

const (
	Mainnet     = internalfoc.Mainnet
	Calibration = internalfoc.Calibration
)

// EthCaller is the read-only eth_call surface the FoC decoders need.
// *chainrpc.LotusRPC satisfies this.
type EthCaller = internalfoc.EthCaller

// Provider is a decoded ServiceProviderRegistry entry.
type Provider = internalfoc.Provider

// PDPOffering mirrors the synapse-sdk PDPOffering type.
type PDPOffering = internalfoc.PDPOffering

// RegistryAddress returns the registry contract address for the network.
func RegistryAddress(n Network) string { return internalfoc.RegistryAddress(n) }

// EnumerateActiveProviderIDs returns all active provider ids, paged.
func EnumerateActiveProviderIDs(ctx context.Context, rpc EthCaller, network Network, pageSize uint64) ([]*big.Int, error) {
	return internalfoc.EnumerateActiveProviderIDs(ctx, rpc, network, pageSize)
}

// GetProvider fetches one provider with its PDP product.
func GetProvider(ctx context.Context, rpc EthCaller, network Network, id *big.Int) (*Provider, error) {
	return internalfoc.GetProvider(ctx, rpc, network, id)
}

// GetActiveProviderCount returns activeProviderCount().
func GetActiveProviderCount(ctx context.Context, rpc EthCaller, network Network) (*big.Int, error) {
	return internalfoc.GetActiveProviderCount(ctx, rpc, network)
}
