# filcensus.reiers.io — scope

**Working title:** filcensus (rename from sp-radar)
**Domain:** filcensus.reiers.io
**Cadence:** 1 snapshot / 48h, on-demand for ad-hoc
**Origin host:** mainnet Lotus node Nicklas provides (libp2p + chain queries are heavy)
**Front-end host:** Cloudflare Pages (or Hetzner static), separate from collector

## Why this is buildable (short version)

Every node we care about advertises itself via libp2p `identify` AgentVersion, plus the on-chain miner state gives us multiaddrs and peer IDs. We connect, run identify, scrape the agent string, optionally probe HTTP/RPC, write a snapshot row. Done. No magic.

## Detection signatures (verified against upstream repos, Wed 2026-05-06)

Pulled from current `main` of each repo. These are the actual UserAgent strings the daemons set:

| Software | Source of truth | Agent string pattern |
|---|---|---|
| Lotus | `lotus/build/buildconstants/params.go:42` (`const UserAgent = "lotus"`) + `build.NodeBuildVersion` | `lotus-<version>` (e.g. `lotus-1.34.1+mainnet+git...`) |
| Forest | `forest/src/libp2p/discovery.rs:183` (`with_agent_version(format!("forest-{...}"))`) | `forest-<version>+git.<hash>` |
| Venus (full node) | `venus/app/submodule/network/network_submodule.go:382` (`libp2p.UserAgent("venus")`) | `venus` (and venus-miner / droplet variants from sibling repos) |
| Curio | curio repo (already in sp-radar) | `curio-<version>` via `boostd-data` style identify |
| Boost | boostd repo | `boost-<version>` |
| Lotus-miner (legacy) | lotus-miner | `lotus-<version>` (same agent as full node, distinguished by SP role) |

**Important caveat:** Venus has multiple binaries that share the codebase. We need to also pull `venus-miner` and `droplet` (the storage/retrieval daemon, ex-`market`) and confirm whether they all advertise just `venus` or carry sub-product strings. I'll do that pass when the collector is wired (cheap to do later, doesn't change architecture).

**Identify protocol on Forest:** uses `ipfs/0.1.0` as the protocol version, which matches what Lotus/Boost/Curio publish, so a single libp2p identify dial works for all of them. Good.

## What we collect (per snapshot)

### Per Storage Provider (the SP fleet, ~750k addresses, ~3-5k active)

**Cheap (chain only):**
- minerID (`f0xxxxx`)
- peerID
- multiaddrs (the SP's public dial-in addresses)
- raw byte power, quality-adjusted power
- sector size
- owner / worker / control addresses
- balance, available balance, vesting funds
- deadline / fault counts, faulty sector count
- active / live / faulty / recovering sector counts
- consensus fault elapsed
- beneficiary address

**Probe (libp2p dial):**
- reachable yes/no, dial RTT
- agent version string (raw)
- detected software stack (curio / boost / lotus-miner / venus-miner / droplet / unknown)
- detected version (parsed)
- supported protocols list (e.g. `/fil/storage/mk/1.2.0`, `/fil/retrieval/transports/1.0.0`, indexer protocols)
- IP(s) resolved from multiaddr
- IP geo: country, region, city, ASN, org (MaxMind GeoLite2 free DB or ipinfo.io)
- reverse DNS

**Optional HTTP probe (when Boost/Curio expose a public HTTP endpoint):**
- HTTP banner (Server header, X-Powered-By)
- TLS cert issuer/subject (often leaks operator identity)

### Per chain node (Lotus / Forest / Venus full nodes)

This is the more interesting and harder bucket. SPs are easy because they're listed on chain. Full nodes aren't.

**How we find them:**
1. Bootstrap from our own node's `Filecoin.NetPeers` and `Filecoin.NetAgentVersion` per peer
2. Walk the libp2p DHT crawl from there (peer-of-peer expansion, capped at e.g. 5k unique peers per snapshot to keep runtime bounded)
3. Optionally: known public RPC endpoints (Glif, ChainStack, Forest snapshot service, Venus team's nodes) hit via JSON-RPC `Filecoin.Version` for ground truth

**What we collect per node:**
- peerID, multiaddrs
- agent version → software (lotus / forest / venus / unknown)
- version
- protocols supported
- IP, ASN, country
- whether it acts as: SP host / bootstrap / archival (deduced from protocol set + known peer lists)

**Caveat that matters:** the libp2p crawl will not find every Filecoin node on earth. Private operator nodes that don't accept inbound connections are invisible. We'll catch the public-facing ones, which is the meaningful set anyway. We're not claiming a total census; we're claiming a public-surface census.

## Snapshot storage

- One JSON snapshot per run: `snapshots/YYYY-MM-DD.json` (full data)
- One CSV digest per run: `snapshots/YYYY-MM-DD.csv` (one row per SP)
- Append-only; never mutate old snapshots
- Optional: SQLite rollup `filcensus.db` for trend queries (versions over time, SP churn, etc.)
- Total size estimate: ~10-30 MB per JSON snapshot uncompressed, gzip well

## Dashboard (filcensus.reiers.io)

Static site, regenerated each snapshot. No backend, no auth, no DB at the edge. CF Pages or plain Hetzner static hosting.

**Pages:**
1. **Overview** — current snapshot summary
   - Software distribution donut (count + power), per node type (SP / chain node), with **logos**
   - Geographic heatmap (SPs by country, power-weighted)
   - Total network power tracked, % covered vs. on-chain total
   - Snapshot timestamp and "next refresh" countdown

2. **Versions** — full version table
   - Per software, per version: count, total power, % of fleet
   - Trend chart: version adoption curves over last N snapshots (e.g. lotus 1.33 → 1.34 rollout rate)
   - "Out of date" callout: SPs running >2 minor versions behind latest

3. **Geography**
   - Map (Leaflet + OSM tiles): each SP a dot, sized by power, colored by software
   - ASN concentration: top 20 ASNs by power, % of network
   - Country concentration: top 20 countries by power, % of network

4. **SP detail** — one row per active SP
   - minerID, peerID, IP(s), country, ASN, agent version, software, power, sector size, faulty sectors, last seen
   - searchable / sortable / filterable client-side
   - per-SP page with history (versions over time, power changes, faults over time)

5. **Methodology**
   - How we detect, what's a guess, known blind spots, how to opt out (operators can rotate peer ID; we won't dox aggressively)

**Logos:** local SVG/PNG assets in `web/assets/logos/`:
- lotus, forest, venus, curio, boost, droplet, venus-miner
- I'll grab these from each project's repo (most have a `docs/logo` or `assets/` folder) and bundle them, attribution noted in `LICENSES.md`. No hotlinking.

## Architecture

```
┌──────────────────────────────┐
│ Mainnet Lotus node (yours)   │
│ - JSON-RPC                   │
│ - libp2p peer access         │
└──────────┬───────────────────┘
           │ FULLNODE_API_INFO
           ▼
┌──────────────────────────────┐
│ filcensus collector          │
│ - Go binary, runs on the     │
│   same host (cron every 48h) │
│ - Phases:                    │
│   1. enumerate SPs (chain)   │
│   2. probe SPs (libp2p)      │
│   3. crawl chain peers       │
│   4. geoip enrich            │
│   5. write snapshot          │
│   6. regenerate dashboard    │
│   7. rsync to web host       │
└──────────┬───────────────────┘
           │ rsync / git push
           ▼
┌──────────────────────────────┐
│ filcensus.reiers.io          │
│ - static HTML/JS/CSS         │
│ - data/*.json snapshots      │
│ - Cloudflare in front        │
└──────────────────────────────┘
```

**Collector budget per run (rough):**
- Chain enumeration: 5-10 min (StateListMiners + StateMinerInfo + StateMinerPower per SP, batched)
- libp2p probes: 30-90 min (5k SPs, 50 concurrent, 10s timeout, retries)
- Chain peer crawl: 15-30 min (DHT walks)
- GeoIP + render: 1-2 min
- **Total: ~1-2 hours per snapshot.** Fits comfortably in a 48h cadence.

## Repo restructure (today)

```
filcensus/
  cmd/
    filcensus/             # main binary (renamed from sp-radar)
    filcensus-render/      # static dashboard renderer
  internal/
    chain/                 # chain queries (StateListMiners, MinerInfo, Power, etc.)
    probe/                 # libp2p dial + identify + protocol enumeration
    crawl/                 # chain-node DHT crawl
    detect/                # agent string → (software, version) parser
    geoip/                 # MaxMind / ipinfo wrapper
    snapshot/              # snapshot file format, write, read
    render/                # template + asset bundling for the static site
  web/
    template/              # html templates
    assets/
      logos/               # node software logos
      css/
      js/
  snapshots/               # generated snapshots (.gitignored except a sample)
  SCOPE.md                 # this file
  README.md
```

## Privacy / ethics

- Public-surface only: we read what nodes broadcast on libp2p. We do not exploit, we do not bypass auth, we do not enumerate private endpoints.
- We publish IPs because they're already public via libp2p multiaddrs. Operators who want to hide should not advertise public dial addrs on chain.
- We follow respectful crawl behavior: 1 dial per peer, configurable rate limit, honor connection close, no protocol abuse.
- Provide an opt-out: if an operator emails us, we'll exclude their peer ID from the published version (still counted in aggregates). Document in Methodology.

## Open questions for Nicklas (won't block first build)

1. **Logos:** should I treat each project's repo logo as fair-use citation or do you want me to email each project for explicit OK before we ship? (My read: fair-use citation is fine for an unaffiliated dashboard with attribution; happy to be more conservative if you want.)
2. **Branding:** filcensus.reiers.io is the domain — do we want a name beyond "Filecoin Network Census" on the page itself, or keep it nerdy and minimal?
3. **Dashboard host:** Cloudflare Pages from a `filcensus-web` repo, or push to the Hetzner static dir at `filcensus.reiers.io`? (CF Pages is faster to ship and free; Hetzner is more sovereign.)
4. **Reachability of the mainnet node:** is it the Hetzner one or somewhere else? When you grant SSH I'll need: hostname, user, FULLNODE_API_INFO path, lotus binary path (if any), available disk space.

## Next steps

1. Rename repo + restructure (this commit)
2. Wire chain enumeration phase end-to-end in dry-run mode
3. Wire libp2p probe phase, run against a small subset (`--max-providers 50`) on local Mac
4. Wire detection parsing with unit tests against synthetic agent strings (lotus / forest / venus / curio / boost / droplet / unknown)
5. Wire crawl + geoip
6. Wire renderer + template + logos
7. Ping Nicklas: "ready, give me SSH to mainnet node"
8. First real snapshot
9. Domain wiring (CF DNS for filcensus.reiers.io → CF Pages or Hetzner)
