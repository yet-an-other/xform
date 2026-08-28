> **Log snapshots need a one-time manual migration.** The panel reads the
> journal through a dedicated `xform` namespace, bounded by ACLs on that
> namespace alone. Installing a release never creates, changes, or removes
> root-owned unit, drop-in, tmpfiles, or ACL configuration — an admin installs
> those deliberately, once, following
> [docs/journal-namespace.md](https://github.com/yet-an-other/xform/blob/main/docs/journal-namespace.md).
> Until then the panel runs normally and only the Log snapshot endpoints report
> their own `access_denied` or `journalctl_unavailable`.
