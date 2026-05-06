package scanner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// LotusRPC is a zero-dependency JSON-RPC client for the Lotus Gateway API.
type LotusRPC struct {
	url   string
	token string
	reqID atomic.Int64
}

func NewLotusRPC(apiInfo string) *LotusRPC {
	ai := ParseAPIInfo(apiInfo)
	url := normalizeRPCURL(ai.Addr)
	return &LotusRPC{url: url, token: ai.Token}
}

// normalizeRPCURL accepts either an http(s)/ws(s) URL or a Lotus multiaddr
// like `/ip4/127.0.0.1/tcp/1234/http` and returns an http(s) URL with /rpc/v1.
func normalizeRPCURL(addr string) string {
	url := addr
	// multiaddr form: /ip4/HOST/tcp/PORT/(http|https|ws|wss) [/optional/path]
	if strings.HasPrefix(url, "/") {
		parts := strings.Split(strings.TrimLeft(url, "/"), "/")
		var host, port, scheme string
		for i := 0; i+1 < len(parts); i += 2 {
			switch parts[i] {
			case "ip4", "ip6", "dns", "dns4", "dns6", "dnsaddr":
				host = parts[i+1]
			case "tcp":
				port = parts[i+1]
			}
		}
		switch {
		case contains(parts, "https"):
			scheme = "https"
		case contains(parts, "wss"):
			scheme = "https"
		case contains(parts, "ws"):
			scheme = "http"
		default:
			scheme = "http"
		}
		if strings.Contains(host, ":") { // ipv6
			host = "[" + host + "]"
		}
		url = scheme + "://" + host + ":" + port
	}
	url = strings.Replace(url, "wss://", "https://", 1)
	url = strings.Replace(url, "ws://", "http://", 1)
	if !strings.Contains(url, "/rpc/") {
		url = strings.TrimRight(url, "/") + "/rpc/v1"
	}
	return url
}

func contains(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

type rpcRequest struct {
	Jsonrpc string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int64         `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (r *LotusRPC) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	body, _ := json.Marshal(rpcRequest{
		Jsonrpc: "2.0",
		Method:  "Filecoin." + method,
		Params:  params,
		ID:      r.reqID.Add(1),
	})
	req, err := http.NewRequestWithContext(ctx, "POST", r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var rr rpcResponse
	if err := json.Unmarshal(data, &rr); err != nil {
		return fmt.Errorf("bad response (HTTP %d): %.200s", resp.StatusCode, data)
	}
	if rr.Error != nil {
		return fmt.Errorf("RPC %d: %s", rr.Error.Code, rr.Error.Message)
	}
	return json.Unmarshal(rr.Result, out)
}

func (r *LotusRPC) StateListMiners(ctx context.Context) ([]string, error) {
	var out []string
	return out, r.call(ctx, "StateListMiners", []interface{}{nil}, &out)
}

type PowerClaim struct {
	RawBytePower    string `json:"RawBytePower"`
	QualityAdjPower string `json:"QualityAdjPower"`
}

type MinerPowerResult struct {
	MinerPower  PowerClaim `json:"MinerPower"`
	TotalPower  PowerClaim `json:"TotalPower"`
	HasMinPower bool       `json:"HasMinPower"`
}

func (r *LotusRPC) StateMinerPower(ctx context.Context, maddr string) (*MinerPowerResult, error) {
	var out MinerPowerResult
	return &out, r.call(ctx, "StateMinerPower", []interface{}{maddr, nil}, &out)
}

type MinerInfoResult struct {
	PeerId      string
	Multiaddrs  [][]byte
	Owner       string
	Worker      string
	Beneficiary string
	Control     []string
	SectorSize  int64
}

// NetPeer is one entry from Filecoin.NetPeers.
type NetPeer struct {
	ID    string   `json:"ID"`
	Addrs []string `json:"Addrs"`
}

// NetPeers returns the connected libp2p peers of the daemon. We use these as
// the seed set for chain-node enumeration. Filecoin.NetPeers returns
// AddrInfo objects with peer ID + multiaddrs.
func (r *LotusRPC) NetPeers(ctx context.Context) ([]NetPeer, error) {
	var out []NetPeer
	if err := r.call(ctx, "NetPeers", []interface{}{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// NetAgentVersion returns the agent string our daemon observed for the given
// peer (i.e. what they announced via libp2p identify). Empty if unknown.
func (r *LotusRPC) NetAgentVersion(ctx context.Context, peerID string) (string, error) {
	var out string
	if err := r.call(ctx, "NetAgentVersion", []interface{}{peerID}, &out); err != nil {
		return "", err
	}
	return out, nil
}

// EthCall executes a read-only EVM call against `to` with the given data
// (0x-prefixed hex). Returns the raw return data hex-encoded with 0x prefix.
//
// This implements foc.EthCaller. Filecoin.EthCall expects a tx-like object
// plus a block param; we use "latest".
func (r *LotusRPC) EthCall(ctx context.Context, to string, dataHex string) (string, error) {
	tx := map[string]string{
		"to":   to,
		"data": dataHex,
	}
	var out string
	if err := r.call(ctx, "EthCall", []interface{}{tx, "latest"}, &out); err != nil {
		return "", err
	}
	return out, nil
}

func (r *LotusRPC) StateMinerInfo(ctx context.Context, maddr string) (*MinerInfoResult, error) {
	var raw struct {
		PeerId               string   `json:"PeerId"`
		Multiaddrs           []string `json:"Multiaddrs"`
		Owner                string   `json:"Owner"`
		Worker               string   `json:"Worker"`
		Beneficiary          string   `json:"Beneficiary"`
		ControlAddresses     []string `json:"ControlAddresses"`
		SectorSize           int64    `json:"SectorSize"`
	}
	if err := r.call(ctx, "StateMinerInfo", []interface{}{maddr, nil}, &raw); err != nil {
		return nil, err
	}
	out := &MinerInfoResult{
		PeerId:      raw.PeerId,
		Owner:       raw.Owner,
		Worker:      raw.Worker,
		Beneficiary: raw.Beneficiary,
		Control:     raw.ControlAddresses,
		SectorSize:  raw.SectorSize,
	}
	for _, b64 := range raw.Multiaddrs {
		if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
			out.Multiaddrs = append(out.Multiaddrs, decoded)
		}
	}
	return out, nil
}
