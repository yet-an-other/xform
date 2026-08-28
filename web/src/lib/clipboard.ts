// copyText puts one exact string on the clipboard and reports whether it
// landed. Callers hand it the value the card displays — a Client ID or a
// canonical VLESS URI — and nothing here trims, escapes, or re-serializes it.
//
// The asynchronous Clipboard API only exists in a secure context, and the
// reverse-proxy deployment shape (ADR-0001) may well be served over plain
// HTTP on a LAN, so a selection copy stands behind it.
export async function copyText(value: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    return selectionCopy(value);
  }
}

function selectionCopy(value: string): boolean {
  const scratch = document.createElement("textarea");
  scratch.value = value;
  scratch.setAttribute("readonly", "");
  scratch.setAttribute("aria-hidden", "true");
  // Off-screen rather than hidden: a display:none element cannot be selected.
  scratch.style.cssText = "position:fixed;top:-9999px;opacity:0";
  document.body.append(scratch);
  try {
    scratch.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    scratch.remove();
  }
}
