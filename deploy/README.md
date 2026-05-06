# filcensus deployment (Hetzner @ 157.180.16.39)

One-way data flow from the mainnet node to the dashboard host. The mainnet
node never reads from the dashboard host.

```
┌────────────────────────────┐
│ Mainnet node (Lotus)        │
│  - filcensus census         │  produces snapshot JSON
│  - filcensus push           │  POSTs JSON over HTTPS
└──────────────┬──────────────┘
               │ TLS, bearer auth
               ▼
┌────────────────────────────────────────────────┐
│ Hetzner host (157.180.16.39)                    │
│   nginx (TLS, vhost routing)                    │
│     ├── filcensus.reiers.io        → static     │
│     └── ingest.filcensus.reiers.io → :8770      │
│                                                 │
│   filcensusd (127.0.0.1:8770)                   │
│     - validates SHA256, schema, network         │
│     - atomic write to /var/lib/filcensus/...    │
│     - re-renders dashboard                      │
│                                                 │
│   /var/lib/filcensus/                           │
│     snapshots/    (.json files, kept forever)   │
│     site/         (rendered HTML, served by nginx) │
└────────────────────────────────────────────────┘
```

## One-time setup on the Hetzner host

### 1. Cross-compile the binaries (on Mac)

```bash
cd /Users/reiers/.openclaw/workspace/sp-radar
GOOS=linux GOARCH=amd64 go build -o /tmp/filcensusd ./cmd/filcensusd
GOOS=linux GOARCH=amd64 go build -o /tmp/filcensus ./cmd/filcensus
scp /tmp/filcensusd /tmp/filcensus root@157.180.16.39:/usr/local/bin/
ssh root@157.180.16.39 'chmod +x /usr/local/bin/filcensusd /usr/local/bin/filcensus'
```

### 2. Create user + dirs + token (on Hetzner)

```bash
ssh root@157.180.16.39 'set -e
  id filcensus 2>/dev/null || useradd -r -s /usr/sbin/nologin filcensus
  mkdir -p /var/lib/filcensus/{snapshots,site} /etc/filcensus
  chown -R filcensus:filcensus /var/lib/filcensus /etc/filcensus
  openssl rand -hex 32 > /etc/filcensus/push-token
  chmod 600 /etc/filcensus/push-token
  chown filcensus:filcensus /etc/filcensus/push-token
'
# Print token so we can stash it in vault on the source host
ssh root@157.180.16.39 'cat /etc/filcensus/push-token'
```

### 3. Install systemd unit

```bash
scp deploy/filcensusd.service root@157.180.16.39:/etc/systemd/system/
ssh root@157.180.16.39 'systemctl daemon-reload && systemctl enable --now filcensusd'
ssh root@157.180.16.39 'systemctl status filcensusd --no-pager'
ssh root@157.180.16.39 'curl -sS http://127.0.0.1:8770/healthz'
```

### 4. DNS (Cloudflare, since reiers.io is on CF registrar)

Add two records to the `reiers.io` zone:

```
filcensus.reiers.io          A   157.180.16.39   (proxied OFF or DNS-only)
ingest.filcensus.reiers.io   A   157.180.16.39   (proxied OFF — direct TLS)
```

DNS-only is required so Let's Encrypt's HTTP-01 challenge reaches our box.

### 5. nginx site + Let's Encrypt (chicken-and-egg)

The full vhost config references SSL certs that don't exist yet, so nginx
won't reload. Two-phase deploy:

**Phase 5a: bootstrap config to satisfy ACME HTTP-01:**

```bash
cat > /tmp/filcensus-bootstrap <<EOF
server {
    listen 80;
    server_name filcensus.reiers.io ingest.filcensus.reiers.io;
    location /.well-known/acme-challenge/ {
        root /var/www/html;
        try_files \$uri =404;
    }
    location / { return 200 "filcensus bootstrap\n"; add_header Content-Type text/plain; }
}
EOF
scp /tmp/filcensus-bootstrap root@157.180.16.39:/etc/nginx/sites-available/filcensus-bootstrap
ssh root@157.180.16.39 'set -e
  ln -sf /etc/nginx/sites-available/filcensus-bootstrap /etc/nginx/sites-enabled/
  nginx -t && systemctl reload nginx
  certbot certonly --webroot -w /var/www/html \
    -d filcensus.reiers.io -d ingest.filcensus.reiers.io \
    --non-interactive --agree-tos -m nicklas.reiersen@gmail.com
'
```

**Phase 5b: swap to real config:**

```bash
scp deploy/nginx-filcensus.conf root@157.180.16.39:/etc/nginx/sites-available/filcensus
ssh root@157.180.16.39 'set -e
  rm /etc/nginx/sites-enabled/filcensus-bootstrap
  ln -sf /etc/nginx/sites-available/filcensus /etc/nginx/sites-enabled/
  nginx -t && systemctl reload nginx
'
```

### 5c. Permissions: nginx (`www-data`) must read `/var/lib/filcensus/site/`

The systemd unit creates `/var/lib/filcensus/` with mode 0750 owned by
`filcensus:filcensus`. nginx workers run as `www-data` and would 403/permission‑
denied without group access:

```bash
ssh root@157.180.16.39 'usermod -a -G filcensus www-data && systemctl restart nginx'
```

### 6. Verify

```bash
curl -sS https://filcensus.reiers.io/   # static dashboard (404 OK if no snapshot yet)
curl -sS https://ingest.filcensus.reiers.io/healthz   # daemon healthcheck → "ok"
```

## Pushing a snapshot

On the mainnet node side (or wherever filcensus runs):

```bash
export PUSH_TOKEN=<paste from /etc/filcensus/push-token>

filcensus census \
  --network mainnet \
  --concurrency 50 \
  --out ~/snapshots

filcensus push \
  --to https://ingest.filcensus.reiers.io/_ingest \
  --token "$PUSH_TOKEN" \
  ~/snapshots/mainnet-$(date -u +%F).json
```

Stash `PUSH_TOKEN` in `~/.config/filcensus/push.env` (chmod 600) on the
source host. Never commit it to a repo.

## Cadence

Per the design decision: snapshots are taken manually every ~48h. A simple
flow:

```bash
# As Nicklas, on the mainnet node side:
filcensus census --network mainnet --out ~/snapshots --render /tmp/preview-site
# review /tmp/preview-site/index.html locally
filcensus push --to https://ingest.filcensus.reiers.io/_ingest \
  ~/snapshots/mainnet-$(date -u +%F).json
```

If you ever want a cron, drop a `systemd --user` timer; the daemon side
already handles it.

## Recovery / rollback

All ingested snapshots are kept under `/var/lib/filcensus/snapshots/`
(never deleted by the daemon). To roll back:

```bash
ssh root@157.180.16.39 'cd /var/lib/filcensus/snapshots
  ln -sfn mainnet-2026-05-04.json mainnet-latest.json.tmp
  mv mainnet-latest.json.tmp mainnet-latest.json
'
ssh root@157.180.16.39 'systemctl restart filcensusd'   # forces re-render
```

## Health check

```bash
curl https://filcensus.reiers.io/
curl https://ingest.filcensus.reiers.io/healthz
ssh root@157.180.16.39 'systemctl status filcensusd --no-pager | head'
ssh root@157.180.16.39 'journalctl -u filcensusd -n 50 --no-pager'
```

## Threat model

- **Bearer token compromise:** rotate on the daemon side
  (`openssl rand -hex 32 > /etc/filcensus/push-token` + `systemctl restart filcensusd`)
  and update the source host's `PUSH_TOKEN`. Past snapshots stay valid.
- **Bad payload:** daemon validates schema_version, network match, SHA256
  before writing. A malformed POST cannot corrupt existing snapshots
  (atomic temp-then-rename).
- **Hetzner host compromise:** the mainnet node never reads from this host,
  so the blast radius stays on the dashboard side. Token can be rotated
  cheaply.
- **DoS:** nginx `client_max_body_size 60M`, daemon `--max-bytes 50M`,
  optional source-IP allowlist via nginx `allow`/`deny`.
