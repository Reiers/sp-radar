# filcensus deployment

One-way data flow from the mainnet node to the dashboard host. The mainnet
node never reads from the dashboard host.

```
┌────────────────────────────┐
│ Mainnet node               │
│  - Lotus daemon            │
│  - filcensus census        │  produces snapshot JSON
│  - filcensus push          │  POSTs JSON over HTTPS
└────────────┬───────────────┘
             │ TLS, bearer auth
             ▼
┌────────────────────────────┐
│ Hetzner host (157.180.16.39)│
│  - Caddy (TLS termination)  │
│  - filcensusd               │  validates + writes JSON
│  - static dashboard         │  rendered into /var/lib/filcensus/site
└────────────────────────────┘
             ▲
             │
       Public HTTPS
             │
┌────────────────────────────┐
│ filcensus.reiers.io          │
│ visitors                     │
└────────────────────────────┘
```

## One-time setup on the Hetzner host

```bash
# 1. Build the daemon binary (on a build host, then scp to the server)
GOOS=linux GOARCH=amd64 go build -o filcensusd ./cmd/filcensusd
scp filcensusd root@157.180.16.39:/usr/local/bin/

# 2. Create the service user + dirs
ssh root@157.180.16.39 'set -e
  useradd -r -s /usr/sbin/nologin filcensus || true
  mkdir -p /var/lib/filcensus/{snapshots,site} /etc/filcensus
  chown -R filcensus:filcensus /var/lib/filcensus /etc/filcensus
  openssl rand -hex 32 > /etc/filcensus/push-token
  chmod 600 /etc/filcensus/push-token
  chown filcensus:filcensus /etc/filcensus/push-token
'

# 3. Install the systemd unit
scp deploy/filcensusd.service root@157.180.16.39:/etc/systemd/system/
ssh root@157.180.16.39 'systemctl daemon-reload && systemctl enable --now filcensusd'

# 4. Drop in the Caddy site config (assumes Caddy is already installed)
scp deploy/Caddyfile.example root@157.180.16.39:/etc/caddy/Caddyfile.d/filcensus.caddy
ssh root@157.180.16.39 'caddy validate --config /etc/caddy/Caddyfile && systemctl reload caddy'

# 5. Print the push token so we can stash it in vault on the source host
ssh root@157.180.16.39 'cat /etc/filcensus/push-token'
```

## DNS

Point both names at the Hetzner box (or via Cloudflare proxy):

```
filcensus.reiers.io          A   157.180.16.39
ingest.filcensus.reiers.io   A   157.180.16.39
```

If using Cloudflare, "Full (strict)" SSL mode and either:
- proxy ON for filcensus.reiers.io (cache + DDoS protection)
- proxy OFF or "DNS only" for ingest.filcensus.reiers.io (so we can pin
  source IP if we want, and so the upload TLS path is direct).

## Pushing a snapshot

On the mainnet node (or wherever filcensus runs):

```bash
export PUSH_TOKEN=<paste from /etc/filcensus/push-token>
filcensus push \
  --to https://ingest.filcensus.reiers.io/_ingest \
  /path/to/mainnet-2026-05-06.json
```

Stash `PUSH_TOKEN` in `~/.config/filcensus/push.env` (chmod 600) on the
source host. Never commit it to a repo.

## Cron / cadence

Per the design decision: snapshots are taken manually every ~48h. A simple
flow:

```bash
# As Nicklas, on the mainnet node side (in his shell history, not cron):
filcensus census --network mainnet --out ~/snapshots --render /tmp/preview-site
# review /tmp/preview-site/index.html locally
filcensus push --to https://ingest.filcensus.reiers.io/_ingest \
  ~/snapshots/mainnet-$(date -u +%F).json
```

If you ever want a cron, drop a `systemd --user` timer; the daemon side is
already designed to handle it.

## Recovery / rollback

All ingested snapshots are kept under `/var/lib/filcensus/snapshots/`
(never deleted by the daemon). To roll the dashboard back to a prior
snapshot, update the symlink and re-render:

```bash
cd /var/lib/filcensus/snapshots
ln -sfn mainnet-2026-05-04.json mainnet-latest.json.tmp
mv mainnet-latest.json.tmp mainnet-latest.json
# Touch the file to trigger inotify if you wired one, or restart filcensusd
systemctl restart filcensusd
```

## Health check

```bash
curl https://filcensus.reiers.io/healthz   # via Caddy → filcensusd is healthy
curl https://ingest.filcensus.reiers.io/_status   # only listed if proxied
```

## Threat model

- Bearer token compromise: rotate on the daemon side
  (`openssl rand -hex 32 > /etc/filcensus/push-token` + restart filcensusd)
  and update the source host's `PUSH_TOKEN`. Past snapshots stay valid.
- Bad payload: daemon validates schema_version, network match, SHA256
  before writing. A malformed POST cannot corrupt existing snapshots
  (atomic temp-then-rename).
- Hetzner host compromise: the mainnet node never reads from this host,
  so the blast radius stays on the dashboard side.
- DoS: Caddy `request_body max_size`, daemon `--max-bytes`, optional
  source-IP allowlist in Caddy.
