# xform

xform is a read-only monitoring panel for a single xray-core proxy server. It observes the host machine, the xray service, and each of the proxy's users, and presents their current state and accumulated usage.

## Language

### The system

**Panel**:
The xform application as a whole — the thing the admin opens in a browser. It observes; it never changes xray.
_Avoid_: dashboard (that's the page), manager, admin console

**xray**:
The xray-core proxy server being monitored, running on the same host as the panel.
_Avoid_: the core, the proxy (alone), the backend

**API**:
The panel's JSON interface used by the dashboard. It exposes current observations and durable history; it never changes xray.
_Avoid_: REST service, backend, control API

**Host**:
The machine running xray and the panel.
_Avoid_: server, node, machine (alone)

**Dashboard**:
The single page the panel presents, showing host stats, xray status, and the users table.
_Avoid_: home page, main screen

**User**:
A client of the xray proxy, identified by an email address. The email IS the identity — renaming an email creates a new user, it does not rename the old one.
_Avoid_: client, account, customer, subscriber

**Gone user**:
A user who no longer exists in xray's configuration but whose history the panel retains. Gone users are hidden by default, never erased.
_Avoid_: deleted user, removed user, inactive user

**Release**:
A published, versioned build of the panel, cut from a git tag. The updater consumes releases, never arbitrary commits.
_Avoid_: build, version (alone)

**Updater**:
The host-side automation that installs the latest release of the panel and restarts it.
_Avoid_: auto-update, agent, cron job (that's its schedule, not the thing)

**Session**:
A successful login's continuing right to use the API, carried by the `xform_session` cookie. It expires 24h after last use and never survives a panel restart.
_Avoid_: login (that's the act that starts one), token, cookie (those are its carrier)

### Metrics

**Traffic**:
Cumulative bytes moved by a user or by xray as a whole, split into uplink and downlink.
_Avoid_: bandwidth, data usage, throughput

**Uplink**:
Bytes flowing from the user toward the proxy (upload).
_Avoid_: upload, tx, upstream traffic

**Downlink**:
Bytes flowing from the proxy toward the user (download).
_Avoid_: download, rx, downstream traffic

**Raw counter**:
xray's own in-memory traffic counter for a user. It resets to zero whenever xray restarts and cannot be trusted across restarts.
_Avoid_: counter (alone), xray total

**Durable total**:
The panel's own accumulated traffic figure for a user, built from raw-counter deltas so it survives xray restarts. This is the number shown as a user's traffic.
_Avoid_: lifetime traffic, persisted counter, real total

**Speed**:
A user's or the server's current transfer rate, derived by the panel from the change in counters over a short window. xray does not provide this natively.
_Avoid_: bandwidth, throughput, rate (alone)

**Last seen**:
The timestamp of a user's most recent activity. xray forgets it the moment the user disconnects; the panel remembers it durably.
_Avoid_: last login, last connection, last active

**Online**:
A user with at least one live connection to xray right now, along with the set of IP addresses they are connected from.
_Avoid_: active, connected, logged in

**Presence**:
The users' online status, current online IPs, and last seen as one concern — live from xray's online RPCs, durable through the store.
_Avoid_: activity tracking, connection state (alone), online stats

### States

**xray status**:
The panel's view of the xray service: running, stopped, or unreachable.
_Avoid_: health, state (alone)

**Stale**:
The mark on user data being served from the panel's last-known snapshot because xray is unreachable. Stale data is shown, but always flagged.
_Avoid_: cached, outdated, frozen

**Degraded**:
The panel's mode when xray is stopped or unreachable — the dashboard stays up, host stats stay live, xray-derived data is stale or absent.
_Avoid_: error mode, offline mode, maintenance mode
