# 📡 filcensus

**Filecoin network census — every 48 hours, who's running what, where.**

> Renamed from `sp-radar`. Same idea, broader scope: now covers full nodes (Lotus / Forest / Venus) in addition to Storage Providers (Curio / Boost / Venus-miner / Droplet / lotus-miner).

filcensus connects to a mainnet Lotus node, enumerates every active SP from chain state, probes each one over libp2p, walks the public peer graph for full nodes, enriches with GeoIP, and renders a static dashboard at **filcensus.reiers.io**.

📋 **See [`SCOPE.md`](./SCOPE.md) for the full plan** (what, why, how, blind spots).

## What it tracks

- **SPs** — software (Curio, Boost, lotus-miner, Venus-miner, Droplet), version, peer ID, multiaddrs, IP, ASN, country, raw/QA power, sector size, faulty sectors
- **Chain nodes** — software (Lotus, Forest, Venus), version, peer ID, IP, ASN, country, role hints
- **FoC nodes** — from the on-chain `ServiceProviderRegistry` contract (`0xf55dDbf63F1b55c3F1D4FA7e339a68AB7b64A5eB` on mainnet): every registered FoC service provider's id, name, serviceURL, declared location, ipniPeerId, active flag. We probe each node's `pdp/ping` endpoint and run libp2p identify on its peerID. **Node enumeration only** — no rails, no settlements, no usage tracking.
- **Geography & ASN concentration** — power-weighted, per country and per ASN
- **Version adoption curves** — trend lines across snapshots

## Detection signatures (verified upstream)

Pulled directly from each project's `main` branch on 2026-05-06:

| Software | Where it's set | Agent string |
|----------|----------------|--------------|
| **Lotus** | `lotus/build/buildconstants/params.go` (`UserAgent = "lotus"`) | `lotus-<version>` |
| **Forest** | `forest/src/libp2p/discovery.rs` (`with_agent_version("forest-{...}")`) | `forest-<version>+git.<hash>` |
| **Venus** | `venus/app/submodule/network/network_submodule.go` (`libp2p.UserAgent("venus")`) | `venus` |
| **Curio** | curio repo libp2p init | `curio-<version>` |
| **Boost** | boostd libp2p init | `boost-<version>` |

Identify protocol on all of them: `ipfs/0.1.0`. One libp2p dial covers the whole fleet.

## Cadence

One snapshot every 48 hours. Each snapshot:
1. Enumerates SPs from chain (~5-10 min)
2. Probes each SP via libp2p (~30-90 min)
3. Walks public peer graph for chain nodes (~15-30 min)
4. GeoIP enriches everything (~1 min)
5. Writes `snapshots/YYYY-MM-DD.json` + `.csv`
6. Regenerates static dashboard
7. Pushes to filcensus.reiers.io

Total runtime: ~1-2 hours per snapshot. Runs as a cron on Nicklas's mainnet Lotus node.

## Status

🚧 **In-progress rename + restructure** (was `sp-radar`).

- [x] Repo cloned, scope written
- [x] Detection signatures verified against upstream (lotus, forest, venus)
- [ ] Rename module path → filcensus
- [ ] Restructure layout per `SCOPE.md`
- [ ] Wire chain enumeration phase
- [ ] Wire libp2p probe phase
- [ ] Wire chain-node DHT crawl
- [ ] Wire GeoIP enrichment
- [ ] Wire static renderer + logos
- [ ] First snapshot on mainnet node Nicklas provides
- [ ] Domain wiring (filcensus.reiers.io)

## Quick start (legacy, pre-rename — still works)

```bash
git clone https://github.com/Reiers/sp-radar.git
cd sp-radar
go build -o sp-radar ./cmd/sp-radar

export FULLNODE_API_INFO="https://api.node.glif.io/rpc/v1"
./sp-radar scan
```

The `serve` subcommand will be replaced by the static-render-and-rsync flow described in `SCOPE.md`.

## Privacy

We read what nodes broadcast publicly via libp2p `identify` and chain-published multiaddrs. We do not exploit, bypass auth, or enumerate private endpoints. Operators who want to be invisible should not advertise public dial addrs on chain. Opt-out by emailing the maintainer — we'll exclude your peer ID from the published version (still counted in aggregates).

## License

MIT
