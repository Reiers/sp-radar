package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Reiers/sp-radar/internal/snapshot"
	"github.com/urfave/cli/v2"
)

// pushCmd uploads a snapshot file to the dashboard host. The flow is
// deliberately one-way:
//
//   mainnet node --(snapshot.json + signature)--> dashboard host
//
// The mainnet node only POSTs; it never reads from the dashboard host. That
// keeps the trust model simple: dashboard host compromise can't reach back
// into the Lotus node.
//
// Auth: shared bearer token (PUSH_TOKEN env var). Compared with constant-time
// equality on the receiver side. Per-snapshot SHA256 is included as a separate
// field so the receiver can verify integrity independently.
var pushCmd = &cli.Command{
	Name:      "push",
	Usage:     "Upload a snapshot JSON to the dashboard host",
	ArgsUsage: "<snapshot.json>",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "to", Usage: "Receiver URL, e.g. https://filcensus.reiers.io/_ingest", Required: true},
		&cli.StringFlag{Name: "token", Usage: "Bearer token (or PUSH_TOKEN env)", EnvVars: []string{"PUSH_TOKEN"}, Required: true},
		&cli.DurationFlag{Name: "timeout", Usage: "Upload timeout", Value: 60 * time.Second},
		&cli.BoolFlag{Name: "dry-run", Usage: "Validate snapshot, print what would be sent, don't actually POST"},
	},
	Action: runPush,
}

func runPush(c *cli.Context) error {
	if c.NArg() != 1 {
		return fmt.Errorf("usage: filcensus push <snapshot.json>")
	}
	path := c.Args().First()

	// Sanity-check the file as an actual snapshot before sending.
	snap, err := snapshot.Read(path)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	if snap.SchemaVersion == 0 {
		return fmt.Errorf("snapshot %s has no schema_version (corrupt?)", path)
	}
	if snap.Network == "" {
		return fmt.Errorf("snapshot %s has no network field", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])

	meta := map[string]string{
		"network":         snap.Network,
		"generated_at":    snap.GeneratedAt.UTC().Format(time.RFC3339),
		"schema_version":  fmt.Sprintf("%d", snap.SchemaVersion),
		"sha256":          digestHex,
		"sps_total":       fmt.Sprintf("%d", snap.Aggregates.SPsTotal),
		"operators_total": fmt.Sprintf("%d", snap.Aggregates.OperatorsTotal),
		"foc_total":       fmt.Sprintf("%d", snap.Aggregates.FoCNodesTotal),
		"chain_nodes":     fmt.Sprintf("%d", snap.Aggregates.ChainNodesTotal),
	}

	fmt.Printf("Snapshot: %s\n", path)
	fmt.Printf("  Network: %s\n", snap.Network)
	fmt.Printf("  Generated: %s\n", snap.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Printf("  Size: %d bytes\n", len(data))
	fmt.Printf("  SHA256: %s\n", digestHex)
	fmt.Printf("  Operators: %d\n", snap.Aggregates.OperatorsTotal)
	fmt.Printf("  FoC: %d\n", snap.Aggregates.FoCNodesTotal)

	if c.Bool("dry-run") {
		fmt.Println("--dry-run: not sending")
		return nil
	}

	// multipart/form-data: fields + the JSON file
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range meta {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile("snapshot", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, bytes.NewReader(data)); err != nil {
		return err
	}
	mw.Close()

	url := strings.TrimRight(c.String("to"), "/")
	req, err := http.NewRequestWithContext(c.Context, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.String("token"))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Filcensus-SHA256", digestHex)
	req.Header.Set("X-Filcensus-Network", snap.Network)
	req.Header.Set("User-Agent", "filcensus-push/"+version)

	client := &http.Client{Timeout: c.Duration("timeout")}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	// Parse server reply if it's JSON.
	var reply struct {
		Stored bool   `json:"stored"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &reply); err == nil && reply.SHA256 != "" {
		if reply.SHA256 != digestHex {
			return fmt.Errorf("server SHA256 mismatch: sent %s, server got %s", digestHex, reply.SHA256)
		}
	}
	fmt.Printf("OK (HTTP %d): %s\n", resp.StatusCode, strings.TrimSpace(string(respBody)))
	return nil
}
