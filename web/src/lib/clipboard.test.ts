import { afterEach, describe, expect, it, vi } from "vitest";

import { copyText } from "./clipboard";

// jsdom implements no execCommand at all, so the selection fallback has to be
// installed before it can be observed.
function stubExecCommand(implementation: () => boolean) {
  const command = vi.fn(implementation);
  Object.defineProperty(document, "execCommand", {
    configurable: true,
    writable: true,
    value: command,
  });
  return command;
}

afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(document, "execCommand");
});

describe("copyText", () => {
  it("writes the exact value through the clipboard API", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });

    // Every character survives: a Connection profile's URI is copied byte for
    // byte or not at all.
    const uri = "vless://id@host:443?type=tcp&encryption=none#alice%40example.com%20%C2%B7%20Main";
    await expect(copyText(uri)).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith(uri);
  });

  it("falls back to a selection copy when the clipboard API is absent", async () => {
    // Plain-HTTP reverse-proxy deployments (ADR-0001) are not secure
    // contexts, so navigator.clipboard simply is not there.
    vi.stubGlobal("navigator", {});
    let copied: string | null = null;
    stubExecCommand(() => {
      copied = document.querySelector("textarea")?.value ?? null;
      return true;
    });

    await expect(copyText("vless://id@host:443")).resolves.toBe(true);
    expect(copied).toBe("vless://id@host:443");
    // The scratch element never outlives the copy.
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("falls back when the clipboard API rejects", async () => {
    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    const command = stubExecCommand(() => true);

    await expect(copyText("vless://id@host:443")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalled();
    expect(command).toHaveBeenCalledWith("copy");
  });

  it("reports failure when neither route works", async () => {
    vi.stubGlobal("navigator", {});
    stubExecCommand(() => false);

    await expect(copyText("vless://id@host:443")).resolves.toBe(false);
    expect(document.querySelector("textarea")).toBeNull();
  });
});
