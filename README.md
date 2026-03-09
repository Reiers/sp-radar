# 📡 SP Radar

**Scan the Filecoin network and see which Storage Providers run Curio, Boost, or Venus.**

SP Radar connects to a Lotus gateway, enumerates all active Storage Providers, then probes each one via libp2p to identify their software stack, power, and retrieval support. Results are shown as a terminal report, exported as JSON, or served as a live dashboard.

Zero Lotus dependency. Works with any public or private gateway.

## Quick Start

```bash
git clone https://github.com/Reiers/sp-radar.git
cd sp-radar
go build -o sp-radar ./cmd/sp-radar

export FULLNODE_API_INFO="https://api.node.glif.io/rpc/v1"
./sp-radar scan
```

## Commands

### `sp-radar scan`

One-shot network scan.

```bash
./sp-radar scan                                # basic scan
./sp-radar scan -c 100 --lotus-concurrency 100 # faster
./sp-radar scan -o json                        # JSON to stdout
./sp-radar scan --json-file results.json       # save JSON
./sp-radar scan -v                             # per-SP details
./sp-radar scan --max-providers 50             # test subset
```

| Flag | Default | Description |
|------|---------|-------------|
| `-c, --concurrency` | 50 | Concurrent libp2p queries |
| `--lotus-concurrency` | 50 | Concurrent Lotus API calls |
| `--max-providers` | 0 (all) | Limit providers to scan |
| `-o, --output` | text | `text` or `json` |
| `--json-file` | - | Write JSON results to file |
| `-v, --verbose` | false | Per-SP details |
| `--timeout` | 10s | Per-SP connection timeout |
| `--api` | `$FULLNODE_API_INFO` | Lotus gateway endpoint |

### `sp-radar serve`

Live dashboard with periodic scans.

```bash
./sp-radar serve --port 8080 --interval 6h
```

Serves a web dashboard at `http://localhost:8080` showing:
- Software distribution (node count + power)
- Percentage breakdown with bars
- Historical trend chart

History is saved to `~/.sp-radar/history/`.

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | 8080 | HTTP port |
| `--interval` | 6h | Scan interval |
| `-c, --concurrency` | 50 | Concurrent libp2p queries |
| `--lotus-concurrency` | 50 | Concurrent Lotus API calls |
| `--timeout` | 10s | Per-SP timeout |
| `--api` | `$FULLNODE_API_INFO` | Lotus gateway |

## Example Output

```
SP Radar - Filecoin Storage Provider Network Scan
==================================================
Scanned: 2026-03-09T14:00:00Z | SPs on chain: 749,777 | Min power: 2,104

Software Distribution:
  Curio        15 SPs      175.42 PiB QAP  (  0.7%)
  Boost       245 SPs     3894.12 PiB QAP  ( 78.2%)
  Venus        31 SPs      385.87 PiB QAP  ( 10.3%)
  Markets       4 SPs        2.10 PiB QAP  (  0.2%)
  Unknown       0 SPs        0.00 PiB QAP  (  0.0%)

Indexer nodes: 35

Agent Versions:
  boost/1.7.6: 100
  lotus/1.34.1: 80
```

## How Detection Works

Two signals:

1. **Agent version** (libp2p identify)
   - Contains `curio` -> Curio
   - Contains `venus` or `droplet` -> Venus

2. **Protocols** (libp2p negotiation)
   - `/fil/storage/mk/1.2.0` -> Boost
   - `/fil/storage/mk/1.1.0` -> Markets (legacy)
   - `/legs/head/` -> Indexer support

Unreachable SPs count as Unknown.

## Requirements

- Go 1.22+
- A Lotus gateway endpoint (Glif, local node, etc.)
- Network access to SPs via libp2p

Public gateways work but are slow for 750K+ miners. A local node is faster.

## Data

```
~/.sp-radar/
  libp2p.key    # auto-generated peer identity
  history/      # scan history JSON (serve mode)
```

## License

MIT
