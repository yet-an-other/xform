# xform

xform is a read-only monitoring panel for a single xray instance. This initial scaffold serves live host CPU, RAM, storage, uptime, and load statistics from a Go API and renders them in an embedded React dashboard.

## Run

Go 1.26 or newer is required. The production dashboard is checked in under `web/dist`, so a normal Go build produces the complete binary:

```sh
go build
./xform
```

The panel listens on `127.0.0.1:9090` by default. Override it with `XFORM_LISTEN`:

```sh
XFORM_LISTEN=0.0.0.0:9090 ./xform
```

Open <http://127.0.0.1:9090>. The dashboard refreshes every five seconds. Live server data is also available from `GET /api/v1/server`.

Authentication and xray/user collection are intentionally not part of this scaffold.

## Develop

```sh
# Go tests and checks
go test ./...
go vet ./...

# Frontend tests, typechecking, and production build
npm ci --prefix web
npm --prefix web test
npm --prefix web run typecheck
npm --prefix web run build
```

After changing the frontend, commit the regenerated `web/dist` assets so `go build` remains self-contained. `go generate ./web` runs `npm ci` and rebuilds those assets.
