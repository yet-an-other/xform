# xform

xform is a read-only monitoring panel for a single xray instance. The Go binary serves the JSON API and collects host stats (CPU, RAM, storage, uptime, load); the React dashboard renders them and polls every five seconds.

## Layout

```
internal/             the Go module (go.mod lives here — the repo root is language-neutral)
internal/api/         JSON API handlers
internal/hoststats/   host-stats collector + 5s snapshot cache
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

Authentication and xray/user collection are intentionally not implemented yet.

## Deployment shapes

Two same-origin shapes are supported (see [ADR-0001](docs/adr/0001-two-same-origin-deployment-shapes.md)):

- **Embedded (default)** — the binary above serves the dashboard itself; nothing else to install.
- **Proxy-hosted** — nginx serves the built dashboard and proxies `/api/*` to the binary on loopback. Reference config: [`deploy/nginx.conf.example`](deploy/nginx.conf.example); systemd unit: [`deploy/xform.service`](deploy/xform.service).

The API emits no CORS headers; the dashboard is always served same-origin.

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
