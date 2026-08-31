# User management — spec draft (wayfinder map #44)

Ready to fold into SPEC.md. Compiled from the map's closed tickets: charting decisions (#45), HandlerService research (#46), dialog prototype (#47), write path (#48), mutation API (#49); rollout notes from #50.

## 1. Summary

The panel manages the user roster: add, edit, and remove users from the dashboard, applied to the running xray immediately, surviving both xray restarts and ansible re-provisioning. The panel's own roster store is the source of truth; every change is rendered into `config.json` **and** pushed live over the xray gRPC API. Ansible no longer manages `clients` lists.

## 2. Domain model

CONTEXT.md is updated: the Panel and API are no longer read-only ("it observes, and it manages the user roster"); the **Roster** is the panel-held source of truth applied to xray; a **Gone user** is one removed from the Roster with history retained; **Roster sync** (synced / pending / failed) names the write-side state, distinct from Stale (read-side).

## 3. Storage model

The roster lives in the panel's SQLite database (`XFORM_DB`), in a new `roster` table, one row per user:

- `email` — the identity; case-insensitively unique. Changing an email is remove + add.
- `client_id` — UUID; unique across the roster.
- `inbounds` — the set of VLESS inbound tags the user is attached to (zero allowed: a profile-less user).
- `created_at`, `updated_at`.

Traffic history is untouched: durable totals stay keyed by email in the existing tables, so remove → gone keeps history, and re-adding the same email rejoins it.

## 4. Apply path

Order per change: **store → file render → API push.**

1. **Store** the change. The mutation API succeeds once stored; apply proceeds asynchronously.
2. **Render** the roster into `config.json` via raw-span surgery: a token scan locates each managed inbound's `settings.clients` array; only those byte spans are rewritten. Key order, unknown fields, and JSONC comments elsewhere stay byte-stable; comments *inside* clients arrays are not preserved (machine-managed arrays). Within an array, existing clients keep their positions; new clients append at the end. Write is atomic: temp file in the same directory + rename, requiring directory write access (§8).
3. **Push** to the running xray over `HandlerService.AlterInbound` (`AddUserOperation` / `RemoveUserOperation`), per affected inbound. Edit diffs old vs new inbound selection: adds to newly attached, removes from detached; a changed Client ID is remove + add on every attached inbound (not atomic — a brief auth gap; xray has no update op). Remove does not terminate the user's established connections; they close naturally.

**No restart reconcile.** The rendered file carries the full roster, so an xray restart — by anyone — comes up correct. The panel re-renders and re-pushes only when the file drifts from the store, detected by the existing config watcher.

**Convergence (store wins, with adoption):** a client found in the config but not in the store is adopted into the store; a store user missing from the config is re-rendered and re-pushed; an inbound removed from the config leaves affected users in the store minus that attachment.

**Flow default** for a newly attached client: copy the flow of the inbound's first existing client; fall back to `xtls-rprx-vision` on reality tcp/xhttp inbounds, empty otherwise.

## 5. Mutation API

- `POST /api/v1/users` — add. Body: `email`, optional `client_id` (absent → server generates UUIDv4), `inbounds`. Not idempotent: existing email → `409`.
- `PATCH /api/v1/users/{email}` — edit. Body fields optional: `inbounds` (set), `client_id` (validate + set). Idempotent.
- `DELETE /api/v1/users/{email}` — remove → gone. Idempotent; already-gone → `204`.

Validation: `email` non-empty and case-insensitively unique; `client_id` a valid UUID and roster-wide unique (xray silently overwrites its auth index on same-UUID-different-email); `inbounds` must name existing VLESS inbounds. Violations → `409 Conflict` with a machine-readable reason: `email_taken` / `client_id_taken` / `unknown_inbound`.

Responses return the full stored roster record plus the current Roster sync state, so the dialog can show "stored, applying…" without a second fetch.

**CSRF**: the session cookie is already `HttpOnly; Secure; SameSite=Lax` (cross-site POSTs don't carry it). Mutations additionally reject requests whose `Origin` / `Sec-Fetch-Site` indicates cross-site. No token ceremony.

## 6. UI

Variant A of the prototype (`docs/prototypes/user-management.html`, branch `prototype/user-management`): row actions + modal dialogs.

- **Add**: `+ Add user` in the Users section header → modal: email, inbound multi-select (tag + protocol · transport · port per option), Client ID pre-generated and editable, ⟳ Generate beside it (`crypto.randomUUID()`, client-side).
- **Edit**: ✎ per row → modal: inbound multi-select + editable Client ID with ⟳ Generate. Email immutable (change = remove + add).
- **Remove**: 🗑 per row → confirm modal: removed immediately from every inbound; history kept; becomes gone.
- **Failure surface**: red banner inside the dialog + `apply failed` badge on the row; the change stays stored and retries (§7). The Users section header shows the Roster sync state when not synced.

## 7. Failure semantics

Roster sync states on the roster: **synced** (store, file, and running xray agree), **pending** (stored, not yet applied), **failed** (last apply failed). Retries fire on every config-watch event and every xray status transition to running; re-saving also retries. No manual retry button.

xray-side facts (research, `docs/research/handlerservice-live-user-management.md`): stats and presence are keyed by email at session time and treat API-added users identically to config clients; API-added users are in-memory only; duplicate-email adds fail case-insensitively.

## 8. Deployment changes

- **Config write access**: the panel's system user needs write on `/usr/local/etc/xray` for atomic temp+rename — `setfacl -m u:xform:rwx /usr/local/etc/xray` (README + ansible).
- **xray API object**: must list `HandlerService` alongside `StatsService` (still loopback-only, no auth/TLS).
- **Ansible contract**: templates never render `clients` lists; ansible keeps inbounds, transport, TLS, ports, routing.

## 9. Rollout

1. Ship the panel with an empty store: first-run adoption imports every existing config user on the first watch tick. No migration step.
2. Strip `clients` from the ansible template.
3. Apply the directory ACL; extend the xray `api` object with `HandlerService`.

## 10. Non-goals

Purging Gone-user history; multiple xray hosts; automating ansible runs. Fog (later maps): per-user flow/level editing, roster audit trail, roster-store backup.
