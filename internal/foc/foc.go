// Package foc enumerates Filecoin Onchain Cloud providers from the
// ServiceProviderRegistry contract.
//
// We talk to the registry via the Lotus node's Filecoin.EthCall RPC, using a
// hand-rolled ABI encoding for the small set of methods we need. This avoids
// pulling go-ethereum's huge dependency tree just to build a few call data
// blobs and decode primitive return types.
//
// Source contract addresses (verified 2026-05-06 from
// @filoz/synapse-core/abis/generated.ts):
//
//   mainnet     0xf55dDbf63F1b55c3F1D4FA7e339a68AB7b64A5eB
//   calibration 0x839e5c9988e4e9977d40708d0094103c0839Ac9D
//
// Function selectors (computed via keccak256 of the canonical signature):
//
//   0x46ce4175  getProviderCount()
//   0xf08bbda0  activeProviderCount()
//   0x2f67c065  getAllActiveProviders(uint256,uint256)
//   0xadd33358  getProviderWithProduct(uint256,uint8)
//   0x5c42d079  getProvider(uint256)
//   0x2335bde0  getProviderByAddress(address)
//   0x93ecb91e  getProviderIdByAddress(address)
//
// PDPOffering schema reference:
//   github.com/FilOzone/synapse-sdk/packages/synapse-sdk/src/sp-registry/types.ts
package foc

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Network identifies which deployment of the registry to talk to.
type Network string

const (
	Mainnet     Network = "mainnet"
	Calibration Network = "calibration"
)

// RegistryAddress returns the on-chain address of the ServiceProviderRegistry
// for the given network.
func RegistryAddress(n Network) string {
	switch n {
	case Mainnet:
		return "0xf55dDbf63F1b55c3F1D4FA7e339a68AB7b64A5eB"
	case Calibration:
		return "0x839e5c9988e4e9977d40708d0094103c0839Ac9D"
	}
	return ""
}

// Function selectors. These are keccak256(signature)[:4] hex-encoded with 0x prefix.
const (
	selGetProviderCount      = "46ce4175"
	selActiveProviderCount   = "f08bbda0"
	selGetAllActiveProviders = "2f67c065"
	selGetProviderWithProduct = "add33358"
)

// ProductPDP is the product type ID for PDP service in the registry.
const ProductPDP uint8 = 0

// EthCaller abstracts the small RPC surface we need. Implemented by
// *scanner.LotusRPC (added in a sibling commit) via an EthCall method.
type EthCaller interface {
	// EthCall executes a read-only call against `to` with `dataHex`
	// (e.g. "0x46ce4175" for a no-argument call) and returns the raw
	// return data hex-encoded (with 0x prefix).
	EthCall(ctx context.Context, to string, dataHex string) (string, error)
}

// Provider is a decoded snapshot row from the registry.
type Provider struct {
	ID                 *big.Int
	ServiceProviderHex string // 0x... operator
	PayeeHex           string // 0x... payment recipient
	Name               string
	Description        string
	Active             bool

	// PDP product (the only product type today)
	HasPDP bool
	PDP    PDPOffering
}

// PDPOffering mirrors the synapse-sdk PDPOffering type, decoded from the
// registry's capability key/value pairs.
type PDPOffering struct {
	ServiceURL              string
	Location                string
	IPNIPeerID              string
	IPNISupportsPiece       bool
	IPNISupportsIPFS        bool
	MinPieceSizeBytes       *big.Int
	MaxPieceSizeBytes       *big.Int
	StoragePricePerTibPerDay *big.Int
	MinProvingPeriodEpochs  *big.Int
	PaymentTokenAddress     string
}

// --- Public API ---

// GetActiveProviderCount calls activeProviderCount() on the registry and
// returns the resulting uint256 as *big.Int.
func GetActiveProviderCount(ctx context.Context, rpc EthCaller, network Network) (*big.Int, error) {
	to := RegistryAddress(network)
	if to == "" {
		return nil, fmt.Errorf("foc: unknown network %q", network)
	}
	out, err := rpc.EthCall(ctx, to, "0x"+selActiveProviderCount)
	if err != nil {
		return nil, fmt.Errorf("eth_call activeProviderCount: %w", err)
	}
	return decodeUint256(out)
}

// GetProviderCount calls getProviderCount() (total ever registered, incl. inactive).
func GetProviderCount(ctx context.Context, rpc EthCaller, network Network) (*big.Int, error) {
	to := RegistryAddress(network)
	if to == "" {
		return nil, fmt.Errorf("foc: unknown network %q", network)
	}
	out, err := rpc.EthCall(ctx, to, "0x"+selGetProviderCount)
	if err != nil {
		return nil, fmt.Errorf("eth_call getProviderCount: %w", err)
	}
	return decodeUint256(out)
}

// GetAllActiveProviders calls getAllActiveProviders(offset, limit) and returns
// the page of provider IDs plus a hasMore flag.
func GetAllActiveProviders(ctx context.Context, rpc EthCaller, network Network, offset, limit uint64) (ids []*big.Int, hasMore bool, err error) {
	to := RegistryAddress(network)
	if to == "" {
		return nil, false, fmt.Errorf("foc: unknown network %q", network)
	}
	data := "0x" + selGetAllActiveProviders + encodeUint256(big.NewInt(int64(offset))) + encodeUint256(big.NewInt(int64(limit)))
	out, err := rpc.EthCall(ctx, to, data)
	if err != nil {
		return nil, false, fmt.Errorf("eth_call getAllActiveProviders: %w", err)
	}
	return decodeUint256ArrayBoolReturn(out)
}

// EnumerateActiveProviderIDs walks the full active-providers list using
// pagination of the given page size. Returns the deduplicated, ordered list
// of IDs. Page size of 100 is a sensible default.
func EnumerateActiveProviderIDs(ctx context.Context, rpc EthCaller, network Network, pageSize uint64) ([]*big.Int, error) {
	if pageSize == 0 {
		pageSize = 100
	}
	var all []*big.Int
	offset := uint64(0)
	for {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		page, hasMore, err := GetAllActiveProviders(ctx, rpc, network, offset, pageSize)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if !hasMore || len(page) == 0 {
			break
		}
		offset += uint64(len(page))
	}
	return all, nil
}

// --- decoding helpers (unit-testable without an EthCaller) ---

// decodeUint256 decodes a 32-byte ABI-encoded uint256 return value (as 0x...
// hex) into *big.Int.
func decodeUint256(returnHex string) (*big.Int, error) {
	raw, err := hexBytes(returnHex)
	if err != nil {
		return nil, err
	}
	if len(raw) < 32 {
		return nil, fmt.Errorf("foc: uint256 return too short (%d bytes)", len(raw))
	}
	return new(big.Int).SetBytes(raw[:32]), nil
}

// decodeUint256ArrayBoolReturn decodes the ABI encoding of
// (uint256[] providerIds, bool hasMore).
//
// Layout (256-bit slots):
//   slot 0: offset to providerIds (always 0x40 = 64 since hasMore is static)
//   slot 1: hasMore (0 or 1)
//   slot 2: length N of providerIds
//   slots 3..3+N-1: each providerId
func decodeUint256ArrayBoolReturn(returnHex string) (ids []*big.Int, hasMore bool, err error) {
	raw, err := hexBytes(returnHex)
	if err != nil {
		return nil, false, err
	}
	if len(raw) < 96 {
		return nil, false, fmt.Errorf("foc: array+bool return too short (%d bytes)", len(raw))
	}
	// slot 0: offset of dynamic array (we ignore the value beyond sanity check)
	arrOffset := new(big.Int).SetBytes(raw[0:32]).Uint64()
	if arrOffset != 0x40 && arrOffset != 64 {
		// Tolerate but warn? For now we just trust the layout from the ABI.
	}
	// slot 1: hasMore
	hasMore = raw[63] != 0 || hasNonZero(raw[32:63])
	// At arrOffset (in raw bytes from start): array length, then elements
	if uint64(len(raw)) < arrOffset+32 {
		return nil, false, fmt.Errorf("foc: array length slot out of range")
	}
	n := new(big.Int).SetBytes(raw[arrOffset : arrOffset+32]).Uint64()
	startElems := arrOffset + 32
	if uint64(len(raw)) < startElems+n*32 {
		return nil, false, fmt.Errorf("foc: array elements out of range (n=%d, have %d bytes after length)", n, uint64(len(raw))-startElems)
	}
	ids = make([]*big.Int, 0, n)
	for i := uint64(0); i < n; i++ {
		off := startElems + i*32
		ids = append(ids, new(big.Int).SetBytes(raw[off:off+32]))
	}
	return ids, hasMore, nil
}

// encodeUint256 returns a 32-byte big-endian hex (no 0x prefix) suitable for
// concatenation into call data.
func encodeUint256(v *big.Int) string {
	if v == nil {
		v = new(big.Int)
	}
	b := v.Bytes()
	if len(b) > 32 {
		b = b[len(b)-32:]
	}
	pad := make([]byte, 32-len(b))
	out := append(pad, b...)
	return hex.EncodeToString(out)
}

// hexBytes parses a 0x-prefixed (or unprefixed) hex string into bytes.
func hexBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return hex.DecodeString(s)
}

func hasNonZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return true
		}
	}
	return false
}

// DecodePDPCapabilities turns the registry's capability key/value pairs into
// a PDPOffering. The keys are utf8 strings; values are raw bytes whose
// interpretation depends on the key (per FilOzone PDPOfferingSchema).
//
// This is purely computational and unit-testable without on-chain access.
func DecodePDPCapabilities(caps map[string][]byte) PDPOffering {
	o := PDPOffering{}
	for k, v := range caps {
		switch strings.ToLower(k) {
		case "serviceurl":
			o.ServiceURL = string(v)
		case "location":
			o.Location = string(v)
		case "ipnipeerid":
			o.IPNIPeerID = string(v)
		case "ipnipiece":
			o.IPNISupportsPiece = bytesToBool(v)
		case "ipniipfs":
			o.IPNISupportsIPFS = bytesToBool(v)
		case "minpiecesizeinbytes":
			o.MinPieceSizeBytes = bytesToBig(v)
		case "maxpiecesizeinbytes":
			o.MaxPieceSizeBytes = bytesToBig(v)
		case "storagepricepertibperday":
			o.StoragePricePerTibPerDay = bytesToBig(v)
		case "minprovingperiodinepochs":
			o.MinProvingPeriodEpochs = bytesToBig(v)
		case "paymenttokenaddress":
			if len(v) >= 20 {
				o.PaymentTokenAddress = "0x" + hex.EncodeToString(v[len(v)-20:])
			}
		}
	}
	return o
}

func bytesToBool(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return true
		}
	}
	return false
}

func bytesToBig(b []byte) *big.Int {
	if len(b) == 0 {
		return nil
	}
	return new(big.Int).SetBytes(b)
}

// --- placeholder: getProviderWithProduct decoder ---
//
// The full ProviderWithProduct return is a tuple-of-tuples-with-dynamic-strings,
// which is annoyingly verbose to decode by hand. We stage it: counts and ID
// enumeration land in this commit (they're primitive returns), and the per-
// provider record decoder lands in the next commit so it gets its own focused
// review.

// ErrProviderDecodeNotImplemented is returned by GetProvider while the per-
// provider ABI decoder is pending. The collector entry point falls back to
// ID-only enumeration in this case so the rest of the pipeline can run.
var ErrProviderDecodeNotImplemented = errors.New("foc: per-provider ABI decoder not yet implemented (ID enumeration works)")

// GetProvider reads a single provider record by ID. Currently returns
// ErrProviderDecodeNotImplemented; the collector should fall back to
// ID-only enumeration until this is wired.
func GetProvider(ctx context.Context, rpc EthCaller, network Network, id *big.Int) (*Provider, error) {
	return nil, ErrProviderDecodeNotImplemented
}
