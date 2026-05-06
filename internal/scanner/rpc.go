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
	url := ai.Addr
	url = strings.Replace(url, "wss://", "https://", 1)
	url = strings.Replace(url, "ws://", "http://", 1)
	if !strings.Contains(url, "/rpc/") {
		url = strings.TrimRight(url, "/") + "/rpc/v1"
	}
	return &LotusRPC{url: url, token: ai.Token}
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
	PeerId     string
	Multiaddrs [][]byte
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
		PeerId     string   `json:"PeerId"`
		Multiaddrs []string `json:"Multiaddrs"`
	}
	if err := r.call(ctx, "StateMinerInfo", []interface{}{maddr, nil}, &raw); err != nil {
		return nil, err
	}
	out := &MinerInfoResult{PeerId: raw.PeerId}
	for _, b64 := range raw.Multiaddrs {
		if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
			out.Multiaddrs = append(out.Multiaddrs, decoded)
		}
	}
	return out, nil
}
