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

## FoC providers (Filecoin Onchain Cloud) — added 2026-05-06

Nicklas pointed at https://github.com/FilOzone/filecoin-pay-explorer. After digging, here's what it gives us and how it slots into filcensus.

### What FoC actually exposes on chain

FoC settles all economic activity through the **Payments contract** (ERC-20 / FIL payment rails):

- **Mainnet Payments contract:** `0x23b1e018F08BB982348b15a86ee926eEBf7F4DAa` (start block 5421336)
- **Calibration:** `0x09a0fDc2723fAd1A7b8e3e00eE5DF73841df55a0`
- **USDFC token** is the primary settlement currency (alongside native FIL)
- The **WarmStorage** + **ServiceProviderRegistry** + **PDP** contracts sit on top of payments and represent the actual storage service layer (FilOzone's FilecoinWarmStorageService, the new dealbot, Storacha, etc.)

The filecoin-pay-explorer subgraph indexes the Payments contract and emits these key entities (from `packages/subgraph/schemas/schema.v1.graphql`):

- **`Operator`** = an Ethereum address that creates and manages payment rails. **In FoC parlance, an Operator IS an FoC service provider.** Examples already known on mainnet (from `apps/explorer/src/constants/known-addresses.ts`):
  - `0x3c1ae7a70a2b51458fcb7927fd77aae408a1b857` → **Storacha**
  - `0x305025d07c1dee47f25a4990179eff2becddca0b` → **DealBot** (current)
  - `0xa5f90bc2aa73a2e0bad4d7092a932644d5dd5d71` → DealBot (legacy)
  - `0x8408502033c418e1bbc97ce9ac48e5528f371a9f` → **FWSS** (FilecoinWarmStorageService)
  - `0x3e4e5f067cfda2f16aade21912b8324c3d9624f8` → Tippy
  - `0xd19d84c77bbb901971e460830e310933a210dbaa` → PinMe
- **`Account`** = payer or payee on a rail (clients on the buy side, SP-controlled wallets on the sell side)
- **`Rail`** = a payment rail between payer and payee, with operator + token + commission rate
- **`Settlement`** / **`OneTimePayment`** = actual on-chain settlements with amounts, timestamps, network fees
- **`PaymentsMetric`** + daily/weekly rollups = network-level totals

### What we get for filcensus

This is gold. The Payments subgraph gives us a **complete economic view of the FoC service layer**, parallel to the libp2p / chain-state view of the storage layer. So filcensus gets a third axis:

1. **SP layer** (libp2p identify on chain-listed miners): Curio / Boost / lotus-miner / Venus-miner / Droplet — *who's running storage software*
2. **Chain-node layer** (libp2p crawl): Lotus / Forest / Venus — *who's running consensus / RPC*
3. **FoC service layer** (Payments subgraph): Operators (Storacha, DealBot, FWSS, etc.) + their Accounts (payee SP wallets) — *who's selling FoC services and getting paid*

### Per-FoC-provider data we'll surface

Queried from the Payments subgraph (Goldsky public endpoint or self-hosted), per `Operator`:

- Operator address + known label (Storacha, DealBot, FWSS, etc. — start with the upstream `known-addresses.ts` list, extend ourselves)
- Total rails created
- Total active rails (RailState = ACTIVE)
- Total settled volume per token (USDFC, FIL)
- Cumulative one-time payment volume
- Commission earned per token
- Unique payers (clients) and unique payees (SPs receiving funds)
- 7d / 30d trend (deltas vs prior snapshots)
- First-seen block, latest activity block

### The bridge to storage (what makes this powerful)

FoC `payee` Accounts are Ethereum addresses, but many of them are **f410f...-style addresses controlled by SPs**, or they're owner/worker wallets the SP exposes. We can build a best-effort mapping by:

1. Resolving `0x...` payee addresses → `f410f...` (delegated f4 address class) — purely arithmetic, no chain query.
2. Cross-referencing those `f410f...` addresses against on-chain miner state: `StateMinerInfo.{owner,worker,beneficiary,controlAddresses}` for every active miner.
3. When we get a hit, we tag the Operator's payee on that rail with a `minerID`.

Result: per-FoC-provider page can show "Storacha settled X USDFC across N rails to M unique SPs, top 10 SPs by received volume: f01234 (Curio v1.x.y in DE/AS9876), f05678 (Boost v1.7.6 in US/AS14061), ...". That's a join nobody else is publishing in one place.

### Implementation

- New collector phase: `internal/foc/` — GraphQL client against the Payments subgraph endpoint (env var `FOC_SUBGRAPH_URL`, default to a public Goldsky endpoint we'll find or self-host).
- Snapshot file gets a new top-level section: `foc.operators[]`, `foc.metrics`, `foc.rails_summary` (we don't store every rail in the snapshot, just per-operator rollups, but link out to filecoin-pay-explorer for drill-down).
- Dashboard gets a new tab/page: **FoC Services** — leaderboard table similar to `filecoin-pay-explorer`'s own "Services Leaderboard", but with the SP-layer cross-reference baked in.
- Logos for FoC providers go in `web/assets/logos/foc/` (storacha, dealbot, fwss, pinme, tippy + room for more).

### Subgraph endpoint sourcing

Fil-pay-explorer ships with placeholder URLs (`api.goldsky.com/api/public/project_xxx/...`). Real options:
- Use the FilOzone-published Goldsky endpoint once we find it (likely linked in their README releases or a GH release tag — I'll dig when wiring).
- Self-host the subgraph on Goldsky free tier (the README has the deploy steps; ~30 min of work).
- Fallback: query the Payments contract directly via `eth_getLogs` from our Lotus node's ETH RPC (works but slower, more code; only if the subgraph is unavailable).

## Open questions for Nicklas (won't block first build)

1. **Logos:** confirmed fair-use citation is fine. Will note attribution in `LICENSES.md`. ✅
2. **Branding:** "Filecoin Network Census" on the page itself. Confirmed. ✅
3. **Snapshot trigger:** confirmed manual every 48h, not cron. Collector will be a CLI subcommand we run by hand (probably from your laptop SSH'd into the mainnet node). We'll talk through the exact flow once it's built.
4. **Dashboard host:** still open, will default to Cloudflare Pages unless you say otherwise.
5. **Reachability of the mainnet node:** to be granted *after* I tell you it's built. ✅

## Next steps

1. Rename repo + restructure
2. Wire chain enumeration phase end-to-end in dry-run mode
3. Wire libp2p probe phase, run against a small subset (`--max-providers 50`) on local Mac
4. Wire detection parsing with unit tests against synthetic agent strings (lotus / forest / venus / curio / boost / droplet / unknown)
5. Wire crawl + geoip
6. **Wire FoC subgraph collector** (new) — `internal/foc/` against the filecoin-pay subgraph
7. **Wire FoC payee → SP minerID mapping** (new) — `f410f` resolution + StateMinerInfo cross-ref
8. Wire renderer + template + logos (incl. FoC provider logos)
9. Ping Nicklas: "ready, give me SSH to mainnet node"
10. First real snapshot — manual
11. Domain wiring (CF DNS for filcensus.reiers.io → CF Pages or Hetzner)
