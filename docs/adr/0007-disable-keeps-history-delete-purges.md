# Disable keeps history, delete purges

The panel had one removal act: flag the roster row gone, keep every trace (ADR-era non-goal: "purging gone-user history"). Two admin needs hide behind that single act — temporarily cutting a user off, and erasing one for good — and conflating them made the second impossible. We split them: **Disable** is the old remove exactly (off every inbound, recoverable, history rejoined on re-add); **Delete** purges the user from every storage — roster row, durable traffic and presence history, rendered config clients, and the running xray's handler — releasing the email and its Client ID claim entirely.

Considered and rejected:

- **Tombstones** (remember deleted emails to block re-adoption): contradicts the point of delete — nothing may remain.
- **Erase-first delete**: with the store empty there is no stored intent to retry from, and a failed render/push strands a live credential in running xray. Instead, delete is **two-phase**: mark `deleting` → apply like a disable through the existing store → file render → API push machine → purge rows and history once synced. Failed applies keep retrying exactly like every other roster change.

Consequence to accept: after a purge nothing remembers the email, so a stale config or hand edit carrying that client gets it **adopted back as a brand-new user** with fresh history (consistent with how any foreign client is treated; the provisioning contract keeps templates from carrying clients at all).

This supersedes the never-erase non-goal; disable inherits every gone-user behavior unchanged.
