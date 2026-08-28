# Journal namespace deployment and verification

How the Panel gets to read logs, and how to prove it reads only what it should.

The Panel shows Log snapshots for exactly two units: itself and the configured
xray service. It reads them by exec'ing `journalctl`, as its own unprivileged
`xform` user, with no journal-group membership at all. What bounds that access
is a dedicated journal namespace named `xform` (IN-DEV-SPEC §5.4): both units
log into it, and the `xform` user holds read ACLs on that namespace's
directories and nothing else.

**The namespace is the boundary.** ACLs are per file, not per entry — an ACL on
the default journal would expose every entry in each multiplexed file it
covers. The namespace is bounded only because exactly two units are assigned to
it. Assign a third and the Panel can read that unit's logs too.

## Prerequisites

| Requirement | Check |
| --- | --- |
| Linux with systemd 245 or newer | `systemctl --version \| head -1` |
| `journalctl` present at an absolute path | `command -v journalctl` |
| ACL support (tools + filesystem) | `getfacl --version`, and `getfacl /var/log/journal` succeeds |
| tmpfiles support | `systemd-tmpfiles --version` |

Namespaces are a systemd 245 feature; on anything older, leave the migration
undone — the Panel still runs (see [Before the migration](#before-the-migration-has-run)).

## Artifacts

| File | Installed as | Purpose |
| --- | --- | --- |
| [`deploy/xform.service`](../deploy/xform.service) | `/etc/systemd/system/xform.service` | Panel unit: `LogNamespace=xform`, plus an `ExecStartPre=` that re-applies the ACLs |
| [`deploy/xray-journal-namespace.conf.example`](../deploy/xray-journal-namespace.conf.example) | `/etc/systemd/system/<xray-unit>.d/10-xform-journal.conf` | Puts xray in the namespace |
| [`deploy/xform-journal-acl.conf`](../deploy/xform-journal-acl.conf) | `/etc/tmpfiles.d/xform-journal-acl.conf` | Read ACLs for the `xform` user on both namespace paths |

## Migration

One-time, as root, on the Host running xray and the Panel. Nothing here copies
historical records: entries written before the restarts stay in the default
journal, where the Panel cannot reach them. New records appear in the namespace
from the restart onward.

Copy the three artifacts to the Host first and run everything below from the
directory holding them:

```sh
scp deploy/xform.service deploy/xray-journal-namespace.conf.example \
    deploy/xform-journal-acl.conf root@HOST:/root/xform-deploy/
ssh root@HOST
cd /root/xform-deploy
```

```sh
# 1. Prerequisites (see the table above).
systemctl --version | head -1        # systemd 245+
command -v journalctl getfacl systemd-tmpfiles

# 2. The canonical xray unit — systemd's Id, not the spelling you type.
unit="$(systemctl show -p Id --value xray.service)"; echo "$unit"

# 3. The Panel unit gains LogNamespace=xform and the ACL ExecStartPre=. Pick
#    ONE of these two — both lines are commented on purpose.
#
#    Existing install — add them as a drop-in, so the Environment= edits in
#    /etc/systemd/system/xform.service (XFORM_PASSWORD lives there) survive.
#    Overwriting the file would discard them and the Panel would refuse to
#    start:
#
#      systemctl edit xform.service
#        [Service]
#        LogNamespace=xform
#        ExecStartPre=-+/usr/bin/systemd-tmpfiles --create /etc/tmpfiles.d/xform-journal-acl.conf
#
#    Fresh install — the shipped unit already carries both:
#
#      install -m 0644 xform.service /etc/systemd/system/xform.service

# 4. xray namespace drop-in, for that canonical unit.
install -d "/etc/systemd/system/${unit}.d"
install -m 0644 xray-journal-namespace.conf.example \
    "/etc/systemd/system/${unit}.d/10-xform-journal.conf"

# 5. ACLs for the xform user on both namespace paths.
install -m 0644 xform-journal-acl.conf /etc/tmpfiles.d/xform-journal-acl.conf

# 6. Reload and restart both services. The namespace and its journald instance
#    (systemd-journald@xform.service) are created on demand by the first unit
#    that logs into it.
systemctl daemon-reload
systemctl restart xform.service "$unit"

# 7. Apply the ACLs now that the namespace directory exists.
systemd-tmpfiles --create /etc/tmpfiles.d/xform-journal-acl.conf

# 8. Run the access checks below. They are part of the migration, not an
#    optional extra: nothing so far proves the Panel can read the namespace,
#    or that it still cannot read anything else.
```

Step 7 comes after step 6 on purpose: `systemd-tmpfiles` cannot set an ACL on a
directory that does not exist yet, and the directory does not exist until
something logs into the namespace. The same ordering problem returns on every
boot for volatile hosts — boot-time tmpfiles processing runs long before
`systemd-journald@xform` creates `/run/log/journal/<machine-id>.xform` — which
is why `xform.service` re-applies the file itself, as root, on every start.

`XFORM_XRAY_UNIT` must resolve through systemd to the same canonical `$unit`.
The Panel refuses to start otherwise, which is the intended failure: a Panel
that cannot name the unit exactly should say so at deploy time.

### What changes for admins

Records for both units leave the default journal:

```sh
journalctl --namespace=xform -u xform.service      # Panel
journalctl --namespace=xform -u "$unit"            # xray
```

`journalctl -u xray.service` keeps showing the records written *before* the
migration, and nothing after it.

### Rollback

Remove the drop-in and the `LogNamespace=` line, then restart both units. Both
services return to the default journal; namespace records stay where they are
and become unreadable to the Panel, which reports its own collection error
while every other Panel feature keeps working.

## Verification

Root-run, on a **disposable Host** — several checks restart services, rotate
journals, and reboot. Each check states what proves it. This is the procedure
IN-DEV-SPEC §9.2 requires; it is deliberately separate from the Go test suite,
which never touches real journal authority.

Set up once — and again after the reboot in check 6, since the shell does not
survive it:

```sh
id="$(cat /etc/machine-id)"
unit="$(systemctl show -p Id --value xray.service)"
run_as_panel() { sudo -u xform "$@"; }   # or: setpriv --reuid=xform --regid=xform --clear-groups --
```

### 1. Namespace and both unit assignments exist

```sh
systemctl show -p LogNamespace --value xform.service     # -> xform
systemctl show -p LogNamespace --value "$unit"           # -> xform
systemctl is-active systemd-journald@xform.service       # -> active
ls -ld "/var/log/journal/$id.xform" "/run/log/journal/$id.xform" 2>/dev/null
```

At least one of the two directories must exist: `/var/log/journal/…` on a Host
with persistent journaling, `/run/log/journal/…` on a volatile one.

### 2. The Panel reads records for both fixed units

```sh
run_as_panel journalctl --namespace=xform -u xform.service -n 1 --no-pager
run_as_panel journalctl --namespace=xform -u "$unit" -n 1 --no-pager
```

Both must print a record and exit 0. An empty result here is not proof of
anything: a zero exit with no entries only means nothing matched.

### 3. The Panel cannot read the default namespace

```sh
run_as_panel journalctl -n 1 --no-pager; echo "exit=$?"
id -nG xform      # must not list systemd-journal, adm, or wheel
```

The read must print no records and exit non-zero (journalctl reports `EACCES`
when it was blocked from every file it tried). Records here would mean the
`xform` user picked up broad journal access — which the group check is there to
catch.

### 4. The Panel cannot read an unrelated unit

```sh
run_as_panel journalctl -u ssh.service -n 1 --no-pager; echo "exit=$?"
run_as_panel journalctl --namespace=xform -u ssh.service -n 1 --no-pager
```

The first must be denied as in check 3. The second must return no records: the
namespace contains only the two assigned units, so an unrelated unit has
nothing in it. If it does print records, some other unit was assigned to the
namespace — that unit's logs are now readable by the Panel.

### 5. Access survives journal rotation

```sh
journalctl --namespace=xform --rotate     # older systemd: systemctl restart systemd-journald@xform.service
systemctl restart xform.service           # write a fresh record
run_as_panel journalctl --namespace=xform -u xform.service -n 1 --no-pager
getfacl "/var/log/journal/$id.xform" 2>/dev/null | grep xform
```

The read must still succeed, which is what proves the *default* ACL is
inherited by newly created journal files rather than having been applied once
to the files that happened to exist.

### 6. Access survives reboot

```sh
reboot
# reconnect, re-run the "Set up once" block above, then:
run_as_panel journalctl --namespace=xform -u xform.service -n 1 --no-pager
run_as_panel journalctl --namespace=xform -u "$unit" -n 1 --no-pager
```

On a persistent Host the directory and its default ACL survive on disk, and
boot-time tmpfiles re-applies the rules. On a volatile Host the directory is
new every boot and boot-time tmpfiles runs too early to see it — what makes
this check pass there is `xform.service`'s own `ExecStartPre=`. Verify it ran
rather than assuming it:

```sh
getfacl "/run/log/journal/$id.xform" 2>/dev/null | grep xform
```

### 7. Both volatile and persistent storage work

Whichever path the Host uses, check the other by flipping storage for the
namespace instance alone — namespace journald instances read their own config:

```sh
mkdir -p /etc/systemd/journald@xform.conf.d
printf '[Journal]\nStorage=volatile\n' > /etc/systemd/journald@xform.conf.d/10-storage.conf
systemctl restart systemd-journald@xform.service xform.service "$unit"
run_as_panel journalctl --namespace=xform -u xform.service -n 1 --no-pager
```

Repeat with `Storage=persistent`. Remove the drop-in afterwards to return to
the Host's own journal configuration. Both must read successfully — this is
what the two path pairs in `xform-journal-acl.conf` exist for. Note that no
manual `systemd-tmpfiles` run appears here: restarting `xform.service` is what
re-applies the ACLs, and that is exactly the mechanism check 6 depends on.

### 8. Old default-namespace records are not exposed

```sh
journalctl -u "$unit" --no-pager | tail -5          # root: pre-migration records
run_as_panel journalctl --namespace=xform -u "$unit" --no-pager | head -5
```

The namespace must contain only records written after the migration restart.
Nothing copies the older ones across, and the Panel must not show them.

### 9. The sandbox still runs the distro journalctl

The checks above run outside the Panel's unit sandbox, so they prove ACLs but
not that `xform.service`'s hardening lets `journalctl` execute. Exercise the
real path through the Panel's own endpoint:

```sh
curl -sS -c /tmp/xform.jar -X POST -H 'Content-Type: application/json' \
    -d '{"password":"change-me"}' http://127.0.0.1:9090/api/v1/login
curl -sS -b /tmp/xform.jar -w '\n%{http_code}\n' http://127.0.0.1:9090/api/v1/logs/panel
```

Expect `200` and a snapshot carrying entries. A `503` with
`"reason": "journalctl_unavailable"` means the sandbox or the configured path
blocked the executable; `"access_denied"` means it ran but could not open the
namespace (checks 1–2 failed). Confirm the hardening was not weakened to get
there:

```sh
systemd-analyze security xform.service
```

No directive from the shipped unit may have been removed or relaxed — a
journalctl this sandbox cannot run is meant to cost the Panel its Log
snapshots, not its isolation.

## Before the migration has run

Ordinary monitoring does not depend on any of this. With no namespace, no
drop-in, and no ACLs, the Panel starts and serves Host stats, xray status, User
observations, and Config snapshots as usual. Only the Log snapshot endpoints
report a failure, and they report their own: `access_denied` when journalctl
runs but cannot open the namespace, `journalctl_unavailable` when the
executable is missing or unusable. Neither turns into a Degraded dashboard, and
neither changes any other source's freshness.

The Panel does refuse to *start* on two configuration errors, both caught at
deploy time rather than at the first log request: an `XFORM_JOURNALCTL` that is
not an absolute, executable, regular file, and an `XFORM_XRAY_UNIT` that
systemd cannot resolve to one canonical `.service` identity.

## The updater stays out of this

[`deploy/xform-update.sh`](../deploy/xform-update.sh) installs release binaries
and restarts the Panel. It never writes a unit, drop-in, tmpfiles, or ACL file:
everything on this page is root-owned configuration an admin installs
deliberately. An update therefore cannot grant the Panel journal access it did
not already have, and cannot take it away — which is also why upgrading to the
release that introduced the namespace does not migrate anything by itself.
