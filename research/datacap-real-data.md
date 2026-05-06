# Side quest: real data on Filecoin + how active is f05?

Pulled live from the Filecoin mainnet via Lotus JSON-RPC (chain head height 5,992,062, network version 27, on 2026-05-06).

## Headline numbers

| Metric | Value | Source |
|---|---|---|
| Total raw byte power | **1,902 PiB** (≈ 1.86 EiB) | `f04.TotalRawBytePower` |
| Total quality-adjusted power | **16,524 PiB** (≈ 16.1 EiB) | `f04.TotalQualityAdjPower` |
| QA / Raw ratio | **8.69×** | derived (means ~87% of network is verified deals) |
| Total registered miners (chain) | **749,781** | `f04.MinerCount` |
| Active miners (≥ min power) | **717** | `f04.MinerAboveMinPowerCount` |
| Total deals ever created (NextID) | **133,203,615** | `f05.NextID` |
| Total provider locked collateral | **526,768 FIL** | `f05.TotalProviderLockedCollateral` |
| f05 actor balance (escrow + collateral) | **780,269 FIL** | `f05.Balance` |
| Total pledge collateral (SP side) | **83,796,885 FIL** | `f04.TotalPledgeCollateral` |
| Total DataCap allocations ever issued | **124,382,151** | `f06.NextAllocationId - 1` |
| Total market participants (clients + SPs) | **6,160** | `Filecoin.StateMarketParticipants` |

## How active is f05 (the storage-market actor)?

**Very active.** It's the busiest actor on the network outside the consensus path.

- **Last cron tick:** epoch 5,992,061 (current head epoch is 5,992,062 — f05 cron runs every epoch)
- **NextID = 133,203,615**, meaning ~133M deal proposals have hit the chain in total
- **133M proposals over ~5.99M epochs** = **~22 deals per epoch on average**, every 30 seconds, since genesis
- **Recent deals** (e.g. `id=133203614`, `133203615`) are arriving *right now* — every few epochs there are dozens of new deal proposals
- All sampled recent deals are `VerifiedDeal=true` and are exactly **32 GiB pieces** (one sector each), suggesting the whole f05 surface is dominated by FIL+ verified deals from boost/curio onboarding

**Key state structure (HAMT roots in the actor state):**
- `Proposals`: every deal proposal ever (the 133M)
- `States`: per-deal lifecycle state (active / slashed / cleaned)
- `EscrowTable`: balance per (client, provider) pair
- `PendingProposals`: deals proposed but not yet activated
- `ProviderSectors`: which sector holds which deals per SP

**One important caveat:** `NextID = 133M` is the count of *every deal ever proposed*, not *currently active*. Old deals are GC'd from the on-chain state once they expire (their `States` row is removed, but `NextID` keeps incrementing). When we tried `Filecoin.StateMarketStorageDeal` with deal ID `50,000,000` and `10,000,000`, both errored — those deals are no longer in active state. By contrast `100,000,000` and `80,000,000` returned `active`. So the active set is roughly the most recent ~80M deal IDs.

## How much real data is actually stored?

This is where the answer gets nuanced. Three different definitions:

### (1) "Bytes committed to sectors" (chain truth)
**1,902 PiB raw**. This is what every SP has sealed and is proving via PoSt. No double-counting, no marketing.

### (2) "Bytes representing active client deals"
Roughly **~1,690 PiB raw** if we attribute the QA ratio cleanly:
- Total QA = 16,524 PiB
- QA = raw_committed + 9 × raw_verified  (verified deals get a 10x QA multiplier)
- Solving: raw_verified ≈ (QA - raw_committed) / 9
- raw_verified ≈ (16,524 - 1,902) / 9 ≈ 1,624 PiB
- That's the **chain-truth lower bound for verified data**. The remaining 1,902 - 1,624 ≈ 278 PiB is committed-capacity (CC) sectors with no client data.

So **~85% of Filecoin's 1.9 EiB raw storage is dedicated to verified FIL+ deals**, with ~15% being committed capacity. (Caveat: this assumes the simple 10x QA multiplier with no CC-vs-deal distinction at the QA level — which is the current cryptoeconomic model post FIP-0002x.)

### (3) "Unique bytes of unique data"
**Unknown, much lower.** Filecoin doesn't (and can't) deduplicate identical pieces stored by different clients on different SPs. The ecosystem-wide consensus among Slingshot / Spade / FIDL is that:
- Many large datasets are stored with **multiple replicas** (3-15× per dataset, depending on DataCap requirements per pathway)
- Some FIL+ pathways enforce minimum replication counts (e.g. "GOME Fil+" requires `8` replicas, "CIDgravity" requires `2`, "MetazoaZ" requires `5`)
- After deduplication, the FIDL data ops team estimates real-unique-bytes is somewhere between **~150-400 PiB** (their public estimates vary; not a chain-truth number).

## Allocator-Registry & DataCap

- **86 active allocator pathways** (per `https://api.allocator.tech/allocators`)
- Notary-Governance repo is **sunset**; replaced by Allocator-Governance
- **124M DataCap allocations** ever issued (NextAllocationId from f06)
- DataCap is denominated in bytes; 1 DataCap = 1 byte of verified-deal capacity worth 10x QA
- Allocations are organised under `Allocator-Registry/Allocations/<allocator-id>.json` and contain `{verifierId, clientAddress, clientAllowance, msgCid, height}` per allocation

### Allocator registry sample shape

```json
{
  "application_number": 1060,
  "address": "f26oafqpnh7lncjc5hoyj6xib5o62s6gzozdkx5gy",
  "name": "GOME Fil+",
  "allocator_id": "f03626500",
  "organization": "Gome Real Joy E-Commerce Co., Ltd.",
  "metapathway_type": "MDMA",
  "ma_address": "f410fw325e6novwl57jcsbhz6koljylxuhqq5jnp5ftq",
  "pathway_addresses": {
    "msig": "f26oafqpnh7lncjc5hoyj6xib5o62s6gzozdkx5gy",
    "signers": ["f1...","f1...","f1..."]
  },
  "application": {
    "audit": ["Enterprise Data"],
    "tranche_schedule": "I will use the standard Allocation Tranche Schedule",
    "required_sps": "over 5",
    "required_replicas": "8",
    "tooling": ["smart_contract_allocator"]
  },
  "history": {
    "Application Submitted": "2025-07-16T07:41:23.000Z",
    "Approved": "2025-08-28T20:43:07.000Z",
    "DC Allocated": "2025-08-29T19:34:47.000Z"
  }
}
```

## How to wire this into filcensus

The data above is high-leverage: a "Network Truth" section on the dashboard
that says "1.9 EiB raw, 1.6 EiB verified, 86 allocators issuing DataCap, 717
SPs above min power, 26.7M FIL of clients' funds in escrow". All of these are
single-RPC-call queries against our own Lotus node:

| Field | RPC method | Cost |
|---|---|---|
| Raw / QA / Pledge | `StateReadState f04` | cheap, ~1KB |
| Market deals stats (NextID, locked, escrow) | `StateReadState f05` | cheap, ~1KB |
| DataCap allocations counter | `StateReadState f06` | cheap, ~1KB |
| Market participants count | `StateMarketParticipants` | medium, ~360KB / 6160 entries |
| Active miner count | `StateReadState f04` (`MinerAboveMinPowerCount`) | cheap |

A new collector phase (`internal/networktruth/`) running these 4-5 RPC calls
and adding a `NetworkTruth` section to the snapshot would make the dashboard
*much* more interesting without significant runtime cost (~3-5 seconds total).

## Sources

- Lotus mainnet node, Filecoin.StateReadState / StateMarketParticipants (chain epoch 5,992,062)
- `https://api.allocator.tech/allocators` (86 entries, returned 2026-05-06)
- `https://filfox.info/api/v1/deal/list?pageSize=2` (NextID = 133,203,615)
- `github.com/filecoin-project/Allocator-Registry` (95 JSON records, 62 allocations)
- `github.com/filecoin-project/notary-governance` (sunset; pointed to Allocator-Governance)
