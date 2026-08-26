Context: [Determine bounded journald access](https://github.com/yet-an-other/xform/issues/20)

# Bounded journald access

## Question and constraints

The Panel needs a manually refreshed snapshot of the latest 500 entries across two system services: `xform.service` and the configured xray service. It must remain unprivileged and must not gain a way to inspect unrelated host logs. This report does not consider live following, search, download, clearing, or caller-selected units.

The current release build matters to the choice. xform is built with `CGO_ENABLED=0` for Linux amd64 and arm64 ([release workflow at `ac2422b`](https://github.com/yet-an-other/xform/blob/ac2422b/.github/workflows/release.yml#L46-L51)), and the product spec calls for a single static binary ([`SPEC.md` at `ac2422b`](https://github.com/yet-an-other/xform/blob/ac2422b/SPEC.md#L25-L28)). Its service already runs as `User=xform` with an empty capability set, `NoNewPrivileges=true`, and `ProtectSystem=strict` ([`deploy/xform.service` at `ac2422b`](https://github.com/yet-an-other/xform/blob/ac2422b/deploy/xform.service)).

## Finding

A journal filter is not an authorization boundary. Both `journalctl` and `sd-journal` first open every journal file allowed by the caller's file permissions, then filter entries. `sd_journal_open()` explicitly says that inaccessible files are silently ignored ([systemd `sd_journal_open(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_open.html)); `journalctl` says it interleaves entries from all accessible files and that `systemd-journal`, `adm`, and `wheel` members can read all journal files ([systemd `journalctl(1)`](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html#Description)). Giving the network-facing Panel membership in any of those groups and hard-coding `-u` prevents an accidental broad query in xform code, but a compromised Panel process can run another query or parse the files itself.

The cleanest deployment that meets the stated boundary is:

1. Put only xray and the Panel in a dedicated journal namespace with the same `LogNamespace=` value.
2. Give the `xform` user read ACLs only on that namespace's journal directories and files. Do not add it to `systemd-journal`, `adm`, or `wheel`.
3. Read the namespace through a fixed, non-shell `journalctl` snapshot command. The command names exactly the two validated service units, requests the latest 500 combined entries, emits an explicit JSON field set, and has process, time, entry-count, and byte limits.

Journal namespaces are independent in both storage and IPC. `LogNamespace=` routes syslog, journal-native, and stdout/stderr records to the named `systemd-journald@.service` instance and its separate data store ([systemd `systemd-journald.service(8)`, Journal Namespaces](https://www.freedesktop.org/software/systemd/man/latest/systemd-journald.service.html#Journal%20Namespaces); [systemd v257 `systemd.exec.xml`, `LogNamespace=`](https://github.com/systemd/systemd/blob/v257/man/systemd.exec.xml#L3900-L3925)). `LogNamespace=` and `sd_journal_open_namespace()` arrived in systemd 245 ([systemd v257 source](https://github.com/systemd/systemd/blob/v257/man/systemd.exec.xml); [systemd `sd_journal_open(3)`, History](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_open.html#History)). This choice therefore sets systemd 245 as a deployment floor and moves these two services' logs out of the default journal view. Administrators must use `journalctl --namespace=<name>` to see them.

If changing xray's log namespace is unacceptable, the sound alternative is a separate, narrow broker. The Panel keeps no journal group or ACL. A local socket-activated helper gets journal read permission and exposes one operation with no caller-supplied unit or filter: return the latest 500 entries for its root-owned configuration of the two units. The helper can use `journalctl` or `sd-journal`; the privilege separation, not the reader API, creates the boundary. This costs another executable, protocol, unit, and package lifecycle.

No deployment that grants the Panel access to the default system journal can also claim to prevent arbitrary journal access from a compromised Panel.

## Access and deployment mechanics

### Groups and ACLs

Upstream systemd stores system journals as files readable by `systemd-journal`; adding a user to that group grants journal-file read access. It also documents ACLs for additional users and groups and shows default ACLs so future rotated files inherit access ([systemd `systemd-journald.service(8)`, Access Control](https://www.freedesktop.org/software/systemd/man/latest/systemd-journald.service.html#Access%20Control)). `SplitMode=uid` does not solve this for xform. Only regular users get per-user files; system users continue to log to the system journal ([systemd `journald.conf(5)`, `SplitMode=`](https://www.freedesktop.org/software/systemd/man/latest/journald.conf.html#SplitMode=)).

Namespace journals live under `/var/log/journal/<machine-id>.<namespace>/` or `/run/log/journal/<machine-id>.<namespace>/` ([systemd `systemd-journald.service(8)`, Files](https://www.freedesktop.org/software/systemd/man/latest/systemd-journald.service.html#Files)). The namespace service creates its persistent directory with `LogsDirectory=journal/%m.%i`, group `systemd-journal`, and mode `02755` ([systemd v257 `systemd-journald@.service.in`](https://github.com/systemd/systemd/blob/v257/units/systemd-journald%40.service.in)). A deployment using namespace isolation must install access and default ACLs for `xform` on both the persistent and volatile namespace paths, including existing files, then test rotation and reboot. systemd's own tmpfiles rules use `a+` and `A+` ACL entries for journal trees; `A+` applies recursively ([systemd v257 `tmpfiles.d/systemd.conf.in`](https://github.com/systemd/systemd/blob/v257/tmpfiles.d/systemd.conf.in); [systemd `tmpfiles.d(5)`](https://www.freedesktop.org/software/systemd/man/latest/tmpfiles.d.html#a)).

ACLs are per file, not per entry. An ACL on the default namespace still exposes every entry in each allowed journal file. A namespace ACL is bounded only because that namespace must contain exactly the two approved services. The deployment contract should reserve the namespace and state that assigning another unit to it grants the Panel access to that unit's logs.

### Existing sandbox

`ProtectSystem=strict` makes the hierarchy read-only; it does not hide readable journal files. systemd documents `InaccessiblePaths=` as the control that hides paths, while `ReadOnlyPaths=` only refuses writes ([systemd `systemd.exec(5)`](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#ReadWritePaths=)). The current empty capability set and read-only filesystem already prevent journal mutation. They do not narrow reads allowed by DAC or ACL.

An in-process `journalctl` inherits the Panel's user, groups, mount namespace, seccomp policy, and `NoNewPrivileges`. The latter guarantees that the process and its children cannot gain privilege through `execve()`, setuid/setgid bits, or file capabilities ([systemd v257 `systemd.exec.xml`, `NoNewPrivileges=`](https://github.com/systemd/systemd/blob/v257/man/systemd.exec.xml#L4615-L4636)). Consequently a setuid or sudo-style `journalctl` escape is incompatible with the current hardening and is the wrong design. A privileged broker must be a separate service.

The later implementation must verify that the shipped `SystemCallFilter=@system-service` permits the distro's dynamically linked `journalctl` on every supported target. It should also use the absolute distro path selected at install time. The panel should fail the log snapshot, not weaken its sandbox, when that executable is missing or blocked.

## Reader mechanisms

### `go-systemd/sdjournal`

`github.com/coreos/go-systemd/v22/sdjournal` is a low-level wrapper over the C `sd-journal` API ([go-systemd v22.7.0 `sdjournal/journal.go`](https://github.com/coreos/go-systemd/blob/v22.7.0/sdjournal/journal.go)). It offers `AddMatch`, `AddDisjunction`, `SeekTail`, `PreviousSkip`, cursors, timestamps, and entry reads. Exact unit matches can be expressed as `_SYSTEMD_UNIT=xform.service OR _SYSTEMD_UNIT=xray.service`; matches on the same field are automatically ORed, and explicit disjunctions can build more complex terms ([systemd `sd_journal_add_match(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_add_match.html)).

This package does not preserve xform's build shape. Its source includes systemd headers through `import "C"`, and its `dlopen` helper uses cgo and `-ldl` ([go-systemd v22.7.0 `sdjournal/journal.go`](https://github.com/coreos/go-systemd/blob/v22.7.0/sdjournal/journal.go); [`internal/dlopen/dlopen.go`](https://github.com/coreos/go-systemd/blob/v22.7.0/internal/dlopen/dlopen.go)). Go excludes files importing `C` when `CGO_ENABLED=0` ([Go `cmd/cgo` documentation](https://pkg.go.dev/cmd/cgo)). Builds need a C compiler and systemd development headers; runtime needs a compatible `libsystemd.so` that go-systemd loads dynamically ([go-systemd v22.7.0 `sdjournal/functions.go`](https://github.com/coreos/go-systemd/blob/v22.7.0/sdjournal/functions.go)).

There are two semantic traps. First, `_SYSTEMD_UNIT=` selects records emitted by a service, but `journalctl -u` also includes PID 1 records about the unit, object records, and coredumps. The documented expansion is a compound expression using `_SYSTEMD_UNIT`, `UNIT` with `_PID=1`, `OBJECT_SYSTEMD_UNIT`, and `COREDUMP_UNIT` ([systemd `journalctl(1)`, examples](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html#Examples); [systemd v257 `journalctl-filter.c`](https://github.com/systemd/systemd/blob/v257/src/journal/journalctl-filter.c)). A direct reader must either reproduce that behavior or explicitly define the feature as service-emitted records only. Second, go-systemd's `GetEntry` stores fields in `map[string]string`, so repeated journal fields overwrite one another ([go-systemd v22.7.0 `sdjournal/journal.go`, `GetEntry`](https://github.com/coreos/go-systemd/blob/v22.7.0/sdjournal/journal.go#L759-L819)).

Direct `sdjournal` has no permission advantage over `journalctl`. Given the static build requirement and the extra filter code, it is not the preferred xform reader.

### Fixed `journalctl` invocation

For a dedicated namespace, the intended command shape is:

```text
/usr/bin/journalctl
  --system
  --namespace=<reserved-namespace>
  --unit=xform.service
  --unit=<validated-canonical-xray.service>
  --lines=500
  --output=json
  --output-fields=__CURSOR,__REALTIME_TIMESTAMP,__MONOTONIC_TIMESTAMP,_BOOT_ID,_SYSTEMD_UNIT,UNIT,OBJECT_SYSTEMD_UNIT,COREDUMP_UNIT,SYSLOG_IDENTIFIER,PRIORITY,MESSAGE
  --no-pager
```

`--system` prevents an accidentally accessible user journal from masking denial of system-journal access. Multiple `--unit=` options form the union of matching units; each unit includes messages emitted by and about it ([systemd `journalctl(1)`, `--unit=`](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html#-u)). `--lines=500` selects the most recent 500 matching events ([systemd `journalctl(1)`, `--lines=`](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html#-n)). Without `--reverse`, journalctl seeks back 500 from the tail and emits forward, so the selected window is oldest to newest; this behavior is visible in systemd v257's implementation ([`journalctl-show.c`](https://github.com/systemd/systemd/blob/v257/src/journal/journalctl-show.c#L216-L240)). Add `--reverse` only if the API contract wants newest first.

`--output-fields=` limits JSON to an allowlist, although `__CURSOR`, both timestamps, and `_BOOT_ID` are always present ([systemd `journalctl(1)`, `--output-fields=`](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html#--output-fields=)). Trusted fields such as `_SYSTEMD_UNIT` are added by journald and cannot be changed by clients; `MESSAGE`, `PRIORITY`, and `SYSLOG_IDENTIFIER` are untrusted user fields ([systemd `systemd.journal-fields(7)`](https://www.freedesktop.org/software/systemd/man/latest/systemd.journal-fields.html)). JSON mode represents duplicate fields as arrays and non-UTF-8 data as byte arrays. By default it replaces fields larger than 4096 bytes with `null`; `--all` disables that protection and can allocate very large objects, so xform should not pass `--all` ([systemd `journalctl(1)`, JSON output](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html#-o)).

Use Go's `os/exec.CommandContext` with separate arguments and no shell. `os/exec` neither invokes a shell nor performs shell glob expansion, and `CommandContext` kills the process when its context expires ([Go `os/exec`](https://pkg.go.dev/os/exec)). Stream newline-separated JSON objects with `encoding/json.Decoder`, which supports a stream of distinct JSON values ([Go `encoding/json.Decoder`](https://pkg.go.dev/encoding/json#Decoder)). Do not use an unbounded `CombinedOutput` or default-size `bufio.Scanner`.

The 500-entry limit is not a byte limit. Put a hard cap on stdout and stderr and abort the child if either exceeds it. `io.LimitReader` stops returning data after a fixed byte count, but code must read one byte beyond the cap, treat truncation as an error, and kill/reap the child rather than return partial JSON ([Go `io.LimitReader`](https://pkg.go.dev/io#LimitReader)). Also enforce exactly 500 decoded objects even if a future or incompatible `journalctl` violates the requested count.

### Unit-name and argument safety

`journalctl --unit=` accepts either a unit or a glob pattern and expands patterns against all unit names in the journal ([systemd `journalctl(1)`, `--unit=`](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html#-u)); its source explicitly calls `unit_name_mangle(... UNIT_NAME_MANGLE_GLOB ...)` and branches on `string_is_glob()` ([systemd v257 `journalctl-filter.c`](https://github.com/systemd/systemd/blob/v257/src/journal/journalctl-filter.c#L233-L270)). Therefore `--unit=` is not exact merely because it is one argv element.

The HTTP request must never supply a unit name. The Panel unit should be a compiled constant. At startup, validate the administrator's configured xray unit as a canonical, concrete `.service` name, reject glob metacharacters and shorthand, and preferably compare it with systemd's canonical `Id` property already read over D-Bus. systemd limits unit names to 255 characters and defines their character set and type suffixes ([systemd `systemd.unit(5)`, String Escaping and unit-name grammar](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html#String%20Escaping%20for%20Inclusion%20in%20Unit%20Names)).

Pass each unit as one attached argument, `--unit=` plus the validated value. This prevents an initial `-` from becoming another option. Use an absolute executable path and a fixed environment. These measures prevent shell and option injection; validation is still required because journalctl itself interprets unit globs.

An alternative exact-emitter query can use positional `_SYSTEMD_UNIT=<value>` matches and `+` for OR. `sd_journal_add_match()` performs exact field matching, not glob matching ([systemd `sd_journal_add_match(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_add_match.html)). That avoids unit pattern expansion but omits PID 1 and coredump messages included by `-u`. The contract must choose which meaning of "logs for a unit" it wants.

## Snapshot, cursor, and denial semantics

The journal is strictly ordered by reception time. Moving to the next entry yields a later entry or one with the same timestamp ([systemd `sd_journal_next(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_next.html)). A tail seek lands after the newest entry and must be followed by `previous`; moving back 500 then iterating forward gives a chronological latest-500 snapshot ([systemd `sd_journal_seek_tail(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_seek_tail.html)). Entries arriving during the subprocess may fall outside the chosen tail point. That is acceptable for manual snapshots, but the contract should describe each response as one best-effort snapshot, not an atomic journal transaction.

`__REALTIME_TIMESTAMP` is wall-clock microseconds since the epoch. Monotonic time is meaningful only together with `_BOOT_ID` because it restarts on boot ([systemd `sd_journal_get_realtime_usec(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_get_realtime_usec.html)). Keep the cursor as an opaque identifier. systemd says cursor strings identify an entry globally and stably, but multiple cursor strings can refer to the same entry and callers must use `sd_journal_test_cursor()` rather than string equality to prove a seek match ([systemd `sd_journal_get_cursor(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_get_cursor.html)). For this non-paginated feature, cursors are useful as UI keys and diagnostics, not as an ordering key or authorization token.

Denial needs an explicit API state distinct from an empty result. The library silently ignores inaccessible files ([systemd `sd_journal_open(3)`](https://www.freedesktop.org/software/systemd/man/latest/sd_journal_open.html)). journalctl's access check returns `EACCES` when files were blocked and none opened, but no error when no journal files exist; JSON output also enables quiet mode internally ([systemd v257 `journal-util.c`](https://github.com/systemd/systemd/blob/v257/src/shared/journal-util.c#L24-L105); [`journalctl.c`](https://github.com/systemd/systemd/blob/v257/src/journal/journalctl.c#L754-L765)). Treat any non-zero child exit, timeout, malformed JSON, byte-cap breach, or more than 500 objects as unavailable. A zero exit with zero objects means an empty match, but cannot by itself prove that the namespace was provisioned. Installation should separately test namespace existence, ACL inheritance, xray and Panel start records, rotation, and reboot.

## Options compared

| Option | Static xform binary | Scope of authority | Main costs | Assessment |
|---|---:|---|---|---|
| `go-systemd/sdjournal` in Panel plus `systemd-journal` group | No | Every readable journal file | cgo, headers, runtime libsystemd, duplicate-field loss, reproduce `-u` filter | Reject |
| `journalctl` in Panel plus `systemd-journal`/`adm`/`wheel` | Yes | All system journals | External executable and parser | Reject. Fixed argv is not a security boundary |
| `journalctl` in Panel plus ACL on default journal files | Yes | All entries in those multiplexed files | ACL maintenance | Reject |
| Dedicated namespace, namespace-only ACL, fixed `journalctl` | Yes | Every entry in the dedicated namespace, intended to be exactly two units | systemd >=245, xray unit change, ACL/rotation provisioning, changed admin command | Preferred |
| Separate narrow broker, Panel has no journal access | Yes | Fixed broker operation; helper itself can read its allowed journals | New executable/protocol/units and security review | Preferred fallback when logs must remain in default namespace |
| systemd upstream `io.systemd.JournalAccess` Varlink service | Yes | Socket group can request arbitrary units and namespaces | Very new upstream feature and broad query API | Not suitable |

The last row is not hypothetical, but it still does not meet this ticket. systemd added `io.systemd.JournalAccess.GetEntries` on 2026-02-21; it accepts caller-provided units, namespace, priority, and limit ([systemd commit `a109189`](https://github.com/systemd/systemd/commit/a109189fabe6a4c307528459f891c2d545361622); [interface source](https://github.com/systemd/systemd/blob/a109189fabe6a4c307528459f891c2d545361622/src/shared/varlink-io.systemd.JournalAccess.c)). Its socket is mode `0660` and group `systemd-journal`, and the service runs as `systemd-journal` ([socket](https://github.com/systemd/systemd/blob/a109189fabe6a4c307528459f891c2d545361622/units/systemd-journalctl.socket); [service](https://github.com/systemd/systemd/blob/a109189fabe6a4c307528459f891c2d545361622/units/systemd-journalctl%40.service)). It is an all-journal query service, not a two-unit broker, and is too new for a practical deployment baseline.

## Decisions for the later contract ticket

1. Choose dedicated namespace or a separate broker. Do not choose broad Panel membership in a journal-reader group.
2. If using a namespace, set its reserved name, systemd minimum version, persistent versus volatile storage behavior, ACL/tmpfiles recipe for both paths, and the admin-facing `journalctl --namespace=` change.
3. Define "unit logs" as either journalctl `-u` semantics, which includes service, PID 1, object, and coredump records, or exact `_SYSTEMD_UNIT` emitter semantics.
4. Define canonical unit resolution and validation. Decide whether aliases and instantiated services are supported. Request-time unit selection remains forbidden.
5. Fix output order, fields, treatment of absent and non-string fields, maximum stdout/stderr bytes, command timeout, and whether a 4096-byte `null` message is shown as truncated or omitted.
6. Define the API distinction between an empty snapshot and unavailable journal access, plus install-time checks for namespace creation, ACL inheritance, rotation, and reboot.
7. If choosing a broker, fix its identity, transport permissions, no-argument request schema, root-owned unit configuration, output cap, sandbox, update coupling, and behavior when either configured unit is invalid.

## Unresolved questions

- May xray and Panel logs leave the default namespace, or must ordinary `journalctl -u xray.service` continue to show them without `--namespace=`?
- Which supported distributions and systemd versions set the deployment floor? Namespace isolation requires at least systemd 245; the exact ACL/tmpfiles packaging must be tested on each supported distribution.
- Should snapshots include systemd lifecycle and coredump records about each service, or only records emitted by the service processes?
- Must instantiated xray services such as `xray@edge.service` and unit aliases work, or may configuration require the canonical `Id` ending in `.service`?
- What byte ceiling and timeout fit the API? The entry count alone is not a memory or latency bound.
- Is volatile-only journaling a supported host configuration, and what should the API report before either service has emitted its first record?
