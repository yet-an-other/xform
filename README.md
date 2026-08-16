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
deploy/               nginx reference config and systemd unit
SPEC.md               panel specification · CONTEXT.md — domain glossary · docs/adr/ — decisions
```

## Run

Go 1.26 or newer is required. The production dashboard is committed under `internal/cmd/xform/dist`, so a normal Go build produces the complete binary:

```sh
cd internal && go build -o ../xform ./cmd/xform
./xform
```

The panel listens on `127.0.0.1:9090` by default (override with `XFORM_LISTEN`). Open <http://127.0.0.1:9090>. Live host data is also available from `GET /api/v1/server`.

Authentication and xray/user collection are intentionally not implemented yet; the `XFORM_PASSWORD`, `XFORM_XRAY_*`, and `XFORM_DB` settings below are wired into the binary but consumed by later slices.

## Configuration

All runtime settings are environment variables (defaults from SPEC.md §7):

| Variable | Default | Purpose |
| --- | --- | --- |
| `XFORM_LISTEN` | `127.0.0.1:9090` | Panel listen address |
| `XFORM_PASSWORD` | — | Login password (placeholder until auth lands) |
| `XFORM_XRAY_API` | `127.0.0.1:8080` | xray gRPC StatsService address |
| `XFORM_XRAY_CONFIG` | `/usr/local/etc/xray/config.json` | xray config file (user roster) |
| `XFORM_DB` | `/var/lib/xform/xform.db` | SQLite database file |
| `XFORM_XRAY_UNIT` | `xray.service` | systemd unit of the xray service |

## Deployment shapes

Two same-origin shapes are supported (see [ADR-0001](docs/adr/0001-two-same-origin-deployment-shapes.md)):

- **Embedded (default)** — the binary above serves the dashboard itself; nothing else to install.
- **Proxy-hosted** — nginx serves the built dashboard and proxies `/api/*` to the binary on loopback. Reference config: [`deploy/nginx.conf.example`](deploy/nginx.conf.example); systemd unit: [`deploy/xform.service`](deploy/xform.service).

The API emits no CORS headers; the dashboard is always served same-origin.

## Install on the host

The panel runs on the same host as xray. Cross-compile on any machine (pure Go, no cgo), copy the binary and unit over, and start the service:

```sh
cd internal && GOOS=linux GOARCH=amd64 go build -o xform ./cmd/xform
scp xform root@HOST:/usr/local/bin/xform
scp ../deploy/xform.service root@HOST:/etc/systemd/system/xform.service
ssh root@HOST 'systemctl daemon-reload && systemctl enable --now xform'
```

Override settings by editing the `Environment=` lines in [`deploy/xform.service`](deploy/xform.service) before copying, or with `systemctl edit xform` afterwards. The unit starts after `xray.service` and creates `/var/lib/xform` for the default database path.

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
