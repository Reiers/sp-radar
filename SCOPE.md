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

## FoC providers (Filecoin Onchain Cloud) — added 2026-05-06, scope-corrected

Nicklas pointed at https://github.com/FilOzone/filecoin-pay-explorer. **Important scope correction (11:46):** we are *not* interested in usage / settlement / rails. We want to count nodes. So Payments / subgraph / rails are out; we use the **ServiceProviderRegistry** contract instead, which is the canonical on-chain list of FoC nodes.

### The right primitive: `ServiceProviderRegistry`

FoC providers must register themselves on-chain to be discoverable. The registry contract holds the list. Real addresses (verified from `@filoz/synapse-core/src/abis/generated.ts` on 2026-05-06):

| Contract | Mainnet (314) | Calibration (314159) |
|---|---|---|
| **ServiceProviderRegistry** | `0xf55dDbf63F1b55c3F1D4FA7e339a68AB7b64A5eB` | `0x839e5c9988e4e9977d40708d0094103c0839Ac9D` |
| FilecoinWarmStorageService (FWSS) | `0x8408502033C418E1bbC97cE9ac48E5528F371A9f` | `0x02925630df557F957f70E112bA06e50965417CA0` |
| PDPVerifier | `0xBADd0B92C1c71d02E7d520f64c0876538fa2557F` | `0x85e366Cf9DD2c0aE37E963d9556F5f4718d6417C` |

### What the registry gives us per provider (this is the data, not estimates)

From `packages/synapse-core/src/sp-registry/types.ts`:

```ts
interface ProviderInfo {
  id: bigint               // sequential providerId
  serviceProvider: Address // on-chain operator
  payee: Address           // payment recipient
  name: string             // self-declared name (e.g. "Storacha", "DealBot")
  description: string
  active: boolean
  products: { PDP?: ServiceProduct }
}

interface PDPOffering {
  serviceURL: string                // e.g. https://pdp.storacha.example/
  minPieceSizeInBytes: bigint
  maxPieceSizeInBytes: bigint
  storagePricePerTibPerDay: bigint
  minProvingPeriodInEpochs: bigint
  location: string                  // self-declared geography
  paymentTokenAddress: Hex          // 0x0 = FIL, else ERC-20
  ipniPiece: boolean
  ipniIpfs: boolean
  ipniPeerId?: string               // libp2p peer ID for indexing
  extraCapabilities?: Record<string, Hex>
}
```

Enumeration entry points (all available on the registry contract):
- `getProviderCount()` and `getActiveProviderCount()` — total nodes
- `getProvidersByProductType(PDP)` — list of all active PDP providers, paginated
- `getProviderWithProduct(providerId)` — full info per ID
- `getProvider(providerId)` / `getProviderByAddress(addr)` — one by one
- `isProviderActive(providerId)` — status check

We call these directly from our mainnet Lotus node via its EVM/ETH-RPC (`eth_call`) using the registry ABI. **No subgraph needed.** Synapse SDK is the reference but we'll just use the ABI in Go (geth `abigen` or hand-rolled call data).

### What we put in the snapshot per FoC node

Nothing about money, nothing about rails. Just the node:

- providerId, serviceProvider address, payee address
- name, description, active flag
- self-declared location
- serviceURL (the actual node endpoint)
- ipniPeerId (libp2p ID, lets us cross-reference into the libp2p layer)
- product type (PDP today, room for more later)
- minPieceSize / maxPieceSize / minProvingPeriod / paymentToken (descriptive, not used)
- **Live probe of `serviceURL/pdp/ping`** — reachable yes/no, response code, banner if any (Server header)
- **GeoIP of resolved serviceURL hostname** — country, ASN, real-vs-declared location mismatch flag
- **libp2p identify on `ipniPeerId`** when present — returns agent string, lets us check whether a registered FoC node is actually running Curio / Boost / etc.

### What this gets us on the dashboard

A **FoC Nodes** section parallel to **SP Nodes** and **Chain Nodes**:

- Total registered FoC providers, total active
- Distribution by self-declared location (country histogram)
- Distribution by *resolved* location (GeoIP) and ASN — highlight when self-declared diverges
- Distribution by underlying software stack (when libp2p identify works on the ipniPeerId or the `serviceURL` ping returns a banner)
- Reachability rate (active-on-chain-but-pingable vs. active-on-chain-but-dead)
- Per-provider row: name, providerId, serviceURL, location declared / resolved, ipniPeerId, agent string, status

### Implementation

- New collector phase: `internal/foc/` — ETH-RPC client against the registry contract via the Lotus node's `Filecoin.EthCall`. Single ABI, paginated enumeration.
- Optional libp2p step: dial the `ipniPeerId` when present, run identify, attach agent string + protocols.
- Optional HTTP step: GET `<serviceURL>/pdp/ping`, capture status + Server header.
- Snapshot section: `foc.providers[]` with one row per registered provider.
- No subgraph dependency, no Goldsky, no payments / rails / settlements / commission tracking. Strictly node enumeration.

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
6. **Wire FoC registry collector** (new) — `internal/foc/` reads ServiceProviderRegistry via ETH-RPC on the Lotus node, enumerates all PDP providers, optionally probes serviceURL + libp2p ipniPeerId
7. Wire renderer + template + logos (incl. FoC provider logos)
8. Ping Nicklas: "ready, give me SSH to mainnet node"
9. First real snapshot — manual
10. Domain wiring (CF DNS for filcensus.reiers.io → CF Pages or Hetzner)
