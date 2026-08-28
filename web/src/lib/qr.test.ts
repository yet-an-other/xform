import { describe, expect, it } from "vitest";

import { encodeQr, qrPathData } from "./qr";
import { decodeQr, modulesFromPathData } from "@/test/qr-decode";

// A canonical VLESS URI the profile module really produces (IN-DEV-SPEC
// §6.3): the longest supported shape, XHTTP over REALITY.
const canonicalURI =
  "vless://1d37a118-4f1b-4dc0-9e3c-3426b07518df@edge.example.com:443?" +
  "type=xhttp&encryption=none&flow=xtls-rprx-vision&security=reality&" +
  "path=%2Fsplit&host=origin.example.com&mode=stream-up&fp=chrome&" +
  "sni=cover.example.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=0123" +
  "#alice%40example.com%20%C2%B7%20Primary";

const utf8 = new TextEncoder();

// jsdom's TextEncoder answers with a Uint8Array from its own realm, which
// never deep-equals one built here — so byte sequences are compared as plain
// arrays, on content alone.
function bytesOf(value: Uint8Array): number[] {
  return [...value];
}

describe("encodeQr", () => {
  it("round trips the exact bytes it was given", () => {
    const bytes = utf8.encode(canonicalURI);

    expect(bytesOf(decodeQr(encodeQr(bytes).modules))).toEqual(bytesOf(bytes));
  });

  it.each([
    ["one byte", "x"],
    ["a short URI", "vless://1d37a118-4f1b-4dc0-9e3c-3426b07518df@a.example:443?type=tcp"],
    ["a long URI", canonicalURI],
    // Nothing re-encodes the payload: multi-byte UTF-8 survives as its own
    // bytes, so a URI is carried the same way whatever it holds.
    ["multi-byte UTF-8", "проверка · ünïcode · 🛰"],
    ["500 bytes", "b".repeat(500)],
  ])("round trips %s", (_name, text) => {
    const bytes = utf8.encode(text);

    expect(bytesOf(decodeQr(encodeQr(bytes).modules))).toEqual(bytesOf(bytes));
  });

  it("grows the symbol with the payload and reports a square matrix", () => {
    const small = encodeQr(utf8.encode("x"));
    const large = encodeQr(utf8.encode(canonicalURI));

    expect(small.size).toBeLessThan(large.size);
    for (const code of [small, large]) {
      expect(code.modules).toHaveLength(code.size);
      expect(code.modules.every((row) => row.length === code.size)).toBe(true);
    }
  });

  it("is deterministic — the same bytes always give the same matrix", () => {
    const bytes = utf8.encode(canonicalURI);

    expect(encodeQr(bytes)).toEqual(encodeQr(bytes));
  });

  it("refuses a payload no QR symbol can carry", () => {
    expect(() => encodeQr(utf8.encode("x".repeat(3_000)))).toThrow(/too long/i);
  });
});

describe("qrPathData", () => {
  it("draws every dark module and nothing else", () => {
    const code = encodeQr(utf8.encode(canonicalURI));

    const redrawn = modulesFromPathData(qrPathData(code), code.size);

    expect(redrawn).toEqual(code.modules);
  });

  it("stays decodable once drawn", () => {
    const bytes = utf8.encode(canonicalURI);
    const code = encodeQr(bytes);

    expect(bytesOf(decodeQr(modulesFromPathData(qrPathData(code), code.size)))).toEqual(
      bytesOf(bytes),
    );
  });
});
