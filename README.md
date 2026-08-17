# xform

xform is a read-only monitoring panel for a single xray instance. The Go binary serves the JSON API and collects host stats (CPU, RAM, storage, uptime, load); the React dashboard renders them and polls every five seconds.

## Layout

```
internal/             the Go module (go.mod lives here — the repo root is language-neutral)
internal/api/         JSON API handlers
internal/hoststats/   host-stats collector + 5s snapshot cache
internal/config/      XFORM_* environment configuration
internal/cmd/xform/   the binary: wiring, embedded dashboard, committed dist/
web/                  pure TypeScript: React 19 + Vite + Tailwind v4 + shadcn/ui
deploy/               nginx reference configs, systemd units, and the updater script
SPEC.md               panel specification · CONTEXT.md — domain glossary · docs/adr/ — decisions
```

## Run

Go 1.26 or newer is required. The production dashboard is committed under `internal/cmd/xform/dist`, so a normal Go build produces the complete binary:

```sh
cd internal && go build -o ../xform ./cmd/xform
XFORM_PASSWORD=change-me ./xform
```

The panel listens on `127.0.0.1:9090` by default (override with `XFORM_LISTEN`). Open <http://127.0.0.1:9090> and log in with the `XFORM_PASSWORD` value. All `/api/*` endpoints except `login`/`healthz` require the `xform_session` cookie (SPEC.md §5).

The xray service status (running/stopped/unreachable, version, uptime) is live from systemd and the xray binary — `XFORM_XRAY_UNIT` is honored. gRPC-based xray stats and user collection are intentionally not implemented yet; `XFORM_XRAY_API` and `XFORM_DB` are wired into the binary but consumed by later slices.

## Configuration

All runtime settings are environment variables (defaults from SPEC.md §7):

| Variable | Default | Purpose |
| --- | --- | --- |
| `XFORM_LISTEN` | `127.0.0.1:9090` | Panel listen address |
| `XFORM_PASSWORD` | none — **required** | Login password (constant-time compare) |
| `XFORM_XRAY_API` | `127.0.0.1:8080` | xray gRPC StatsService address |
| `XFORM_XRAY_CONFIG` | `/usr/local/etc/xray/config.json` | xray config file (user roster) |
| `XFORM_DB` | `/var/lib/xform/xform.db` | SQLite database file |
| `XFORM_XRAY_UNIT` | `xray.service` | systemd unit of the xray service |

## Deployment shapes

Two same-origin shapes are supported (see [ADR-0001](docs/adr/0001-two-same-origin-deployment-shapes.md)):

- **Embedded (default)** — the binary above serves the dashboard itself; nothing else to install.
- **Embedded, fronted by a proxy** — nginx terminates TLS and proxies everything to the binary, which still serves the embedded dashboard. Reference config: [`deploy/nginx-all-proxy.conf.example`](deploy/nginx-all-proxy.conf.example).
- **Proxy-hosted** — nginx serves the built dashboard and proxies `/api/*` to the binary on loopback. Reference config: [`deploy/nginx.conf.example`](deploy/nginx.conf.example); systemd unit: [`deploy/xform.service`](deploy/xform.service).

The API emits no CORS headers; the dashboard is always served same-origin.

**Subpath mounting**: the dashboard is built mount-point agnostic (relative asset and API URLs), so either shape can hang under a subpath of an existing vhost (e.g. `https://HOST/xform/`) instead of a dedicated one. The proxy strips the prefix before requests reach the binary — no Go changes needed — and the bare subpath must redirect to its trailing-slash form. Commented subpath variants ship in both reference configs.

## Install on the host

The panel runs on the same host as xray. Cross-compile on any machine (pure Go, no cgo), copy the binary and unit over, and start the service:

```sh
cd internal && GOOS=linux GOARCH=amd64 go build -o xform ./cmd/xform
scp xform root@HOST:/usr/local/bin/xform
scp ../deploy/xform.service root@HOST:/etc/systemd/system/xform.service
ssh root@HOST 'useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin xform'
ssh root@HOST 'systemctl daemon-reload && systemctl enable --now xform'
```

The service runs as the dedicated, unprivileged `xform` system user (if it already exists, `useradd` exits non-zero without side effects — safe to re-run). It reads xray's unit state over the D-Bus system bus and runs the world-readable xray binary for the version, so it needs no privileges; the unit file is hardened accordingly (`ProtectSystem=strict`, `NoNewPrivileges=`, … — see the comments in [`deploy/xform.service`](deploy/xform.service)). Upgrading from a root-run install? Re-own the state directory once: `chown -R xform:xform /var/lib/xform`.

Override settings by editing the `Environment=` lines in [`deploy/xform.service`](deploy/xform.service) before copying, or with `systemctl edit xform` afterwards. The unit starts after `xray.service` and creates `/var/lib/xform` (owned by the `xform` system user) for the default database path.

**xray config read access**: once the config-parse slice lands, the panel reads `XFORM_XRAY_CONFIG` (default `/usr/local/etc/xray/config.json`), which is often root-only. Grant the panel's system user read access — either via a group:

```sh
ssh root@HOST 'chgrp xform /usr/local/etc/xray/config.json && chmod 0640 /usr/local/etc/xray/config.json'
```

or via an ACL: `setfacl -m u:xform:r /usr/local/etc/xray/config.json`. Either way the file's directory (`/usr/local/etc/xray`, typically `0755`) must stay traversable by the `xform` system user.

### xray prerequisites

xray must expose per-user and system stats to its loopback gRPC API. Merge this into the xray config (SPEC.md §2):

```json
{
  "stats": {},
  "policy": {
    "levels": { "0": { "statsUserUplink": true, "statsUserDownlink": true, "statsUserOnline": true } },
    "system": {
      "statsInboundUplink": true, "statsInboundDownlink": true,
      "statsOutboundUplink": true, "statsOutboundDownlink": true
    }
  },
  "api": { "tag": "api", "listen": "127.0.0.1:8080", "services": ["StatsService"] }
}
```

Every user must have an `email` in the xray config — per-user stats don't exist without it. Keep xray's gRPC API on loopback: it has no auth/TLS, so loopback binding plus StatsService-only is the entire security model. xray-core ≥ v26.4.13 is recommended (minimum ≥ v24.11.11 for online-user RPCs).

Access the panel through the reverse proxy shape above or an SSH tunnel (`ssh -L 9090:127.0.0.1:9090 HOST`) — it serves plain HTTP on loopback; TLS terminates at the proxy.

### Automatic updates

Pushing a `v*` tag builds and publishes a GitHub release with `xform-linux-amd64`, `xform-linux-arm64`, and `checksums.txt` (see [ADR-0004](docs/adr/0004-tag-gated-releases-with-sha-verified-auto-updater.md)). The host installs new releases itself once the updater is in place:

```sh
scp deploy/xform-update.sh root@HOST:/usr/local/sbin/xform-update
scp deploy/xform-update.service deploy/xform-update.timer root@HOST:/etc/systemd/system/
ssh root@HOST 'chmod 0755 /usr/local/sbin/xform-update && systemctl daemon-reload && systemctl enable --now xform-update.timer'
```

The timer runs daily (`Persistent=true`, so a host that was off catches up). The script skips quietly when the local binary already matches the release's checksums; otherwise it verifies, swaps atomically, restarts `xform.service`, and health-checks it — the previous binary stays at `/usr/local/bin/xform.prev`. Watch it with `journalctl -u xform-update.service`. Roll back by hand: `mv /usr/local/bin/xform.prev /usr/local/bin/xform && systemctl restart xform`.

Run it once by hand after installing (`systemctl start xform-update.service`) to verify the path end to end.

## Develop

```sh
# Go tests and checks (from the module directory)
cd internal && go test ./... && go vet ./...

# Frontend tests, typechecking, and production build (into internal/dashboard/dist)
npm ci --prefix web
npm --prefix web test
npm --prefix web run typecheck
npm --prefix web run build

# Live development: run the binary, then Vite dev server (proxies /api to it)
./xform &
npm --prefix web run dev
```

After changing the frontend, commit the regenerated `internal/cmd/xform/dist` so `go build` remains self-contained. `go generate ./cmd/xform` (from `internal/`) runs `npm ci` and rebuilds it.

CI runs the checks above on every push (`.github/workflows/ci.yml`); pushing a `v*` tag runs the release pipeline (`.github/workflows/release.yml`) that rebuilds the dashboard from source and publishes the static binaries the updater consumes.
