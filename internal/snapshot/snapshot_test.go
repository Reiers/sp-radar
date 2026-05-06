package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteRead_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")

	in := &Snapshot{
		Network:     "mainnet",
		GeneratedAt: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
		GeneratedBy: "filcensus@test",
		ChainHead:   ChainHead{Height: 4_700_000},
		SPs: []SPRecord{
			{
				MinerID:         "f01234",
				PeerID:          "12D3KooWExample",
				Reachable:       true,
				AgentVersion:    "curio-1.24.4",
				Software:        "curio",
				SoftwareVersion: "1.24.4",
				HasMinPower:     true,
				RawBytePower:    "100000000000000",
			},
		},
		ChainNodes: []ChainNodeRecord{
			{
				PeerID:          "12D3KooWChain",
				Software:        "forest",
				SoftwareVersion: "0.30.0",
				Reachable:       true,
			},
		},
		FoCNodes: []FoCNodeRecord{
			{
				ProviderID:         "1",
				ServiceProviderHex: "0xabc",
				PayeeHex:           "0xdef",
				Name:               "Storacha",
				Active:             true,
				ProductType:        "PDP",
				ServiceURL:         "https://pdp.example/",
				DeclaredLocation:   "DE",
				IPNIPeerID:         "12D3KooWFoC",
				HTTPReachable:      true,
				HTTPStatusCode:     200,
			},
		},
		Aggregates: Aggregates{
			SPsTotal:        1,
			SPsReachable:    1,
			SPsBySoftware:   map[string]int{"curio": 1},
			ChainNodesTotal: 1,
			ChainNodesBySW:  map[string]int{"forest": 1},
			FoCNodesTotal:   1,
			FoCNodesActive:  1,
		},
	}

	if err := Write(path, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if out.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", out.SchemaVersion, SchemaVersion)
	}
	if out.Network != "mainnet" {
		t.Errorf("Network roundtrip: got %q", out.Network)
	}
	if len(out.SPs) != 1 || out.SPs[0].MinerID != "f01234" {
		t.Errorf("SPs roundtrip: got %+v", out.SPs)
	}
	if len(out.FoCNodes) != 1 || out.FoCNodes[0].Name != "Storacha" {
		t.Errorf("FoCNodes roundtrip: got %+v", out.FoCNodes)
	}
	if out.Aggregates.SPsBySoftware["curio"] != 1 {
		t.Errorf("Aggregates roundtrip: got %+v", out.Aggregates)
	}
}

func TestWrite_AtomicTmpFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := Write(path, &Snapshot{Network: "calibration"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	// no .tmp leftover
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover .tmp file: %s", e.Name())
		}
	}
}
