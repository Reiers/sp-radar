# 📡 filcensus

**Filecoin Network Census — every 48 hours, who's running what, where.**

> Repo still named `sp-radar` on GitHub for now (renaming would break clones); the Go module + binary + dashboard are all `filcensus`. We can rename the GitHub repo whenever you want.

filcensus connects to a mainnet Lotus node, enumerates every active SP from chain state, probes each one over libp2p, walks the public peer graph for full nodes, reads the on-chain ServiceProviderRegistry for FoC providers, enriches with GeoIP, and renders a static dashboard at **filcensus.reiers.io**.

📋 **See [`SCOPE.md`](./SCOPE.md) for the full plan** (what, why, how, blind spots).

## What it tracks

- **SPs** — software (Curio / Boost / lotus-miner / Venus-miner / Droplet), version, peer ID, multiaddrs, IP, ASN, country, raw/QA power, sector size
- **Chain nodes** — software (Lotus / Forest / Venus), version, peer ID, IP, ASN, country (via NetPeers + NetAgentVersion on our own node)
- **FoC nodes** — from the on-chain `ServiceProviderRegistry` contract: provider id, name, serviceURL, declared location, ipniPeerId, active flag, plus a live HTTP probe of `<serviceURL>/pdp/ping`
- **Geography & ASN concentration** — power-weighted, per country and per ASN
- **Declared-vs-resolved location match** — surface FoC providers whose declared country doesn't match where their serviceURL actually resolves

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

## On-chain contracts (mainnet)

Pulled from `@filoz/synapse-core/abis/generated.ts` on 2026-05-06:

| Contract | Address |
|---|---|
| ServiceProviderRegistry | `0xf55dDbf63F1b55c3F1D4FA7e339a68AB7b64A5eB` |
| FilecoinWarmStorageService (FWSS) | `0x8408502033C418E1bbC97cE9ac48E5528F371A9f` |
| PDPVerifier | `0xBADd0B92C1c71d02E7d520f64c0876538fa2557F` |

## Quick start

```bash
git clone https://github.com/Reiers/sp-radar.git
cd sp-radar
go build -o filcensus ./cmd/filcensus

# Smoke test against calibration via Glif (no auth needed)
export FULLNODE_API_INFO="https://api.calibration.node.glif.io/rpc/v1"
./filcensus foc-count --network calibration

# Full census on calibration (no FoC HTTP probes blocked, ~15s)
./filcensus census --network calibration --skip-sps --skip-chain-nodes \
  --out snapshots --render site

# Open site/index.html in your browser
```

## Full mainnet snapshot

Designed to run on a host with a local Lotus mainnet node (NetPeers / authenticated EthCall require it). Total runtime: ~1–2 hours per snapshot.

```bash
export FULLNODE_API_INFO="<token>:/ip4/127.0.0.1/tcp/1234/http"  # local lotus
export MAXMIND_CITY_DB=/var/lib/GeoIP/GeoLite2-City.mmdb
export MAXMIND_ASN_DB=/var/lib/GeoIP/GeoLite2-ASN.mmdb

./filcensus census \
  --network mainnet \
  --concurrency 100 \
  --lotus-concurrency 100 \
  --out snapshots \
  --render site
```

## Cadence

One snapshot every 48 hours, **manually triggered** by us (per scope decision). Each snapshot:
1. Enumerates SPs from chain (~5–10 min)
2. Probes each SP via libp2p (~30–90 min)
3. Walks NetPeers for chain nodes (~30 s)
4. Reads ServiceProviderRegistry for FoC nodes (~30 s)
5. HTTP-probes each FoC `serviceURL/pdp/ping` in parallel (~1 min)
6. Resolves DNS + GeoIP enriches everything (~1 min)
7. Writes `snapshots/<network>-<YYYY-MM-DD>.json`
8. Renders static dashboard

## Status

- [x] Repo cloned, scope written
- [x] Detection signatures verified against upstream (lotus, forest, venus)
- [x] internal/detect: 9-software classifier, 24 tests
- [x] internal/snapshot: schema v1 file format, 2 tests
- [x] internal/foc: ServiceProviderRegistry enumeration with real keccak selectors and full ABI tuple decoder, 11 tests
- [x] internal/geoip: MaxMind GeoLite2 + caching, 3 tests
- [x] internal/probe: /pdp/ping HTTP prober, 6 tests
- [x] internal/spscan: SP enumeration writing the new record format, 1 test
- [x] internal/chaincrawl: NetPeers + NetAgentVersion chain-node enumeration, 2 tests
- [x] internal/render: embedded static dashboard with logos
- [x] cmd/filcensus: end-to-end runner — census + foc-count subcommands
- [x] Verified end-to-end against calibration (22 FoC providers fully decoded + HTTP-probed)
- [x] Verified mainnet FoC count against Glif (27 active providers)
- [ ] First real snapshot on Nicklas's mainnet node (waiting on SSH access)
- [ ] Domain wiring (filcensus.reiers.io)
- [ ] Operational cron + rsync to dashboard host

## Privacy

We read what nodes broadcast publicly via libp2p `identify`, chain-published multiaddrs, and on-chain registry entries. We do not exploit, bypass auth, or enumerate private endpoints. Operators who want to be invisible should not advertise public dial addrs on chain. Opt-out by emailing the maintainer — we'll exclude your peer ID from the published version (still counted in aggregates).

## License

MIT
