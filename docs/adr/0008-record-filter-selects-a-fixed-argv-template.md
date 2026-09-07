# The log record filter selects a compiled-in journalctl argv

xray writes its whole log — Access records and System records alike — to stdout, so the journal stamps every line info and an ordinary traffic flood pushes every System record out of the bounded Log snapshot: measured on the live host, the newest 500 xray records were 100% Access records. A viewer filter therefore cannot live in the browser: hiding Access records in the dialog would render an empty viewer exactly when an incident is underway. The filter has to live where the snapshot is collected, inside the journal traversal, so a filtered snapshot is the newest 500 **System records wherever they sit in the journal**, not the survivors of the newest 500 raw records.

The xray log endpoint accepts one optional enum parameter, `filter=all` (default) or `filter=system`, and the value selects one of two **compiled-in argv templates** — the same shape as the existing fixed source choice. No caller-controlled string ever reaches journalctl: an unknown value is rejected as `invalid_request`, and a filter value never becomes an argument. The system-only template adds exactly one fixed argument, an attached `--grep=` carrying a pinned PCRE2 negative lookahead:

```text
--grep=^(?![0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)? from )
```

The classification is deliberately strict: an Access record is a timestamp followed by `from ` with no `[Level]` marker, so crash output and every other unmarked line stays visible as a System record. The snapshot response echoes the applied filter, because journalctl reports no skipped-record count and an honest "N of M hidden" is impossible; the echo is what lets the dialog label a filtered view truthfully. Bounds (500 records, byte caps, deadline, one global process slot) and every stable failure reason are unchanged, because filtering happens inside the same traversal the bounds already govern. Measured on the live host: a filtered fetch scans a 40 MB namespaced journal in ~0.13 s.

Considered and rejected:

- **`journalctl --invert` with a positive access pattern**: reads better, but `--invert` is absent on the deployed systemd (259), so the negative lookahead inverts instead. PCRE2 support in journalctl is a pinned deployment requirement (SPEC §8), never probed at runtime.
- **Client-side filtering of the collected snapshot**: cannot reach back past the snapshot window — the flood has already evicted the System records by the time the browser sees anything — and a re-cut of a cached snapshot would violate ADR-0006's "collected when an admin asks".
- **A caller-supplied pattern or severity threshold**: a pattern argument is caller-controlled text reaching journalctl, which the bounded-journald-access threat model forbids; severity views are out of scope because the toggle hides Access records only.
- **Per-entry kind fields and hidden-record counts ("N of M")**: journalctl reports no skipped-record count, so the number would be a lie; severity badges already come from the message parser.
- **Silencing access logging in the xray config**: destroys the Access records an admin may still want; the toggle keeps both views one click apart.

Consequence to accept: the relaxation is scoped to the xray endpoint alone, so the panel endpoint keeps rejecting every parameter — two endpoints with two different parameter rules, where one rule for both would be simpler to state but would pretend Access records exist in the panel journal.
