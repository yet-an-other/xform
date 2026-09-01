// The apply-failure surface shared by the user dialogs (user-management spec
// §6): the change is stored and retries continue — said once, in red.
export function ApplyFailedBanner() {
  return (
    <p
      role="alert"
      className="border-destructive/40 bg-destructive/10 text-destructive mb-4 rounded-lg border px-3 py-2.5 text-xs"
    >
      Apply failed — the change is stored and retries automatically.
    </p>
  );
}
