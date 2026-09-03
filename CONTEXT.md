# xform

xform is a monitoring panel for a single xray-core proxy server. It observes the host machine, the xray service, and each of the proxy's users, presents their current state and accumulated usage — and it manages the roster of users, with changes applied immediately.

## Language

### The system

**Panel**:
The xform application as a whole — the thing the admin opens in a browser. It observes, and it manages the user roster.
_Avoid_: dashboard (that's the page), manager, admin console

**xray**:
The xray-core proxy server being monitored, running on the same host as the panel.
_Avoid_: the core, the proxy (alone), the backend

**API**:
The panel's JSON interface used by the dashboard. It exposes current observations and durable history, and the mutations that manage the roster.
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

**Client ID**:
An xray credential used by a User to connect. It is distinct from the User's email identity.
_Avoid_: User ID, ID (alone), UUID (alone)

**Connection profile**:
A client-ready VLESS connection for one User through one matching xray inbound. The User's email and the inbound's tag identify it.
_Avoid_: connection, link, share link, export

**Advertised connection settings**:
The public endpoint and client-side transport and security values needed for a Connection profile. They describe how a User reaches xray from outside the Host and may differ from the inbound's local settings.
_Avoid_: overrides, profile config, connection metadata

**Disabled user**:
A user removed from the Roster whose history the panel retains. Disabled users are hidden by default, never erased; erasing one is a separate act — see Deleted user. Re-enabling, or re-adding the same email, revives it and rejoins the history.
_Avoid_: gone user, deleted user, inactive user, removed user

**Deleted user**:
A user purged from every storage — the Roster, the panel's own history, the config file, and the running server. Nothing remembers the email; if it reappears it is adopted as a brand-new user with fresh history.
_Avoid_: erased user, purged user, gone user (that is the recoverable one)

**Roster**:
The set of users the panel manages, held by the panel as the source of truth and applied to xray — rendered into the config file and pushed to the running server so both stay in step. Foreign clients found in the config are adopted into the Roster; Roster users missing from the config are re-applied. The Roster supplies the protocol · security labels and decides who is — or becomes — a disabled user.
_Avoid_: config users, client list

**Observation**:
Data the Panel gathers on its own schedule and holds onto — host stats, xray status, and each User's traffic and presence. When a fresh gather fails, the last good one is served and marked stale.
_Avoid_: poll, sample, reading

**Operational snapshot**:
A Log snapshot or Config snapshot: gathered only when an admin asks for it, held no longer than the Viewer showing it, and never part of the Panel's own history (ADR-0006). The counterpart to an Observation.
_Avoid_: operational view, viewer data, log dump

**Watched source**:
A file on the Host that the Panel re-reads whenever it changes and keeps the last valid parse of — the xray config and the Advertised connection settings. A failed re-read never empties a watched source: it keeps the last valid value and marks it stale.
_Avoid_: config watcher, file loader, reloader

**Config snapshot**:
The exact text of the configured xray file read when requested. It is distinct from the parsed Roster.
_Avoid_: parsed config, formatted config, config export

**Log snapshot**:
The latest bounded set of journal entries for the Panel or xray, collected when requested. It is a point-in-time view, never a live stream.
_Avoid_: live logs, log stream, log tail

**Viewer**:
One operational snapshot together with the dialog that asks for it and shows it: Panel logs, xray logs, or xray config. Each viewer reports only its own result — a failed viewer says so on its own, without making any other viewer or the Dashboard look broken.
_Avoid_: modal (that is its presentation), log window

**Collection**:
One request for data a dialog shows, plus what the browser keeps of it while that dialog is open — the last value, whether a refresh failed, and nothing at all once it closes. Spans both shapes: a Viewer collects an operational snapshot once, the User details dialog collects Observations on a cadence.
_Avoid_: fetch, loader, query

**Release**:
A published, versioned build of the panel, cut from a git tag. The updater consumes releases, never arbitrary commits.
_Avoid_: build, version (alone)

**Updater**:
The host-side automation that installs the latest release of the panel and restarts it.
_Avoid_: auto-update, agent, cron job (that's its schedule, not the thing)

**Session**:
A successful login's continuing right to use the API, carried by the `xform_session` cookie. It expires 24h after last use and never survives a panel restart.
_Avoid_: login (that's the act that starts one), token, cookie (those are its carrier)

**Panel uptime**:
The elapsed time since the current Panel process started. It resets whenever the Panel restarts.
_Avoid_: service uptime, host uptime, xray uptime

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
A user with at least one live connection to xray right now, along with the set of IP addresses they are connected from. Disabled and deleted users are never online.
_Avoid_: active, connected, logged in

**Presence**:
The users' online status, current online IPs, and last seen as one concern — live from xray's online RPCs, durable through the store.
_Avoid_: activity tracking, connection state (alone), online stats

**Flag**:
The country flag shown beside an online IP in the users table. Derived live from xray's geoip.dat (ADR-0005), never persisted; private and unknown IPs have none.
_Avoid_: country icon, geo label

### States

**xray status**:
The panel's view of the xray service: running, stopped, or unreachable.
_Avoid_: health, state (alone)

**Roster sync**:
The write-side state of the Roster: synced (store, config file, and running xray agree), pending (a change is stored but not yet applied), or failed (the last apply failed; retries continue on watch fires and xray status transitions). Shown on the Users section. Distinct from Stale, which marks read-side last-good data.
_Avoid_: dirty, unsaved, out of sync

**Stale**:
The mark on observations or source-derived data served from the Panel's last-valid snapshot because the current query, read, or parse failed. User observations become stale when xray is unreachable; parsed xray config and Advertised connection settings become stale when a reload fails. Each source reports freshness independently. Stale data is shown, but always flagged.
_Avoid_: cached, outdated, frozen

**Degraded**:
The panel's mode when xray is stopped or unreachable — the dashboard stays up, host stats stay live, xray-derived data is stale or absent.
_Avoid_: error mode, offline mode, maintenance mode
