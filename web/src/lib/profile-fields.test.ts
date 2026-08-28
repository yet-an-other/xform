import { describe, expect, it } from "vitest";

import { securityValues, transportValues } from "./profile-fields";
import type { ConnectionSecurity, ConnectionTransport } from "./api";

// One case per supported transport and security shape (IN-DEV-SPEC §6.2), so
// every branch a card can print is exercised rather than only the two shapes
// the dialog tests happen to use.
describe("transportValues", () => {
  it.each<[string, ConnectionTransport, [string, string][]]>([
    ["TCP carries nothing of its own", { type: "tcp" }, []],
    [
      "WebSocket",
      { type: "ws", path: "/socket", host: "origin.example.com" },
      [
        ["path", "/socket"],
        ["host", "origin.example.com"],
      ],
    ],
    [
      "HTTPUpgrade",
      { type: "httpupgrade", path: "/upgrade", host: "origin.example.com" },
      [
        ["path", "/upgrade"],
        ["host", "origin.example.com"],
      ],
    ],
    [
      "gRPC",
      { type: "grpc", service_name: "xform.Profile", mode: "multi", authority: "grpc.example.com" },
      [
        ["service", "xform.Profile"],
        ["mode", "multi"],
        ["authority", "grpc.example.com"],
      ],
    ],
    [
      "gRPC without an authority",
      { type: "grpc", service_name: "reality.Profile", mode: "gun" },
      [
        ["service", "reality.Profile"],
        ["mode", "gun"],
      ],
    ],
    [
      "XHTTP",
      { type: "xhttp", path: "/split", host: "origin.example.com", mode: "stream-up" },
      [
        ["path", "/split"],
        ["host", "origin.example.com"],
        ["mode", "stream-up"],
      ],
    ],
  ])("reads %s", (_name, transport, expected) => {
    expect(transportValues(transport)).toEqual(expected);
  });

  it("leaves XHTTP extra to the URI", () => {
    const values = transportValues({
      type: "xhttp",
      path: "/split",
      host: "origin.example.com",
      mode: "packet-up",
      extra: { headers: { "X-Trace": "1" }, scMaxEachPostBytes: 1_000_000 },
    });

    // The URI canonicalizes extra with RFC 8785 (§6.3 rule 12); re-serializing
    // it here could disagree with the `extra=` shown on the same card.
    expect(values.map(([label]) => label)).not.toContain("extra");
    expect(JSON.stringify(values)).not.toContain("X-Trace");
  });
});

describe("securityValues", () => {
  it.each<[string, ConnectionSecurity, [string, string][]]>([
    [
      "TLS at its plainest",
      { type: "tls", fingerprint: "chrome", server_name: "edge.example.com" },
      [
        ["fingerprint", "chrome"],
        ["SNI", "edge.example.com"],
      ],
    ],
    [
      "TLS with every optional field",
      {
        type: "tls",
        fingerprint: "chrome",
        server_name: "edge.example.com",
        alpn: ["h2", "http/1.1"],
        ech: "AEX+DQBB",
        certificate_pins: ["sha256/abc", "sha256/def"],
        verify_name: "verify.example.com",
      },
      [
        ["fingerprint", "chrome"],
        ["SNI", "edge.example.com"],
        // Lists join the way the URI joins them, with commas.
        ["ALPN", "h2, http/1.1"],
        ["ECH", "AEX+DQBB"],
        ["pins", "sha256/abc, sha256/def"],
        ["verify name", "verify.example.com"],
      ],
    ],
    [
      "REALITY",
      {
        type: "reality",
        fingerprint: "chrome",
        server_name: "cover.example.com",
        public_key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
        short_id: "0123",
        post_quantum_verify: "1",
        spider_x: "/spider",
      },
      [
        ["fingerprint", "chrome"],
        ["SNI", "cover.example.com"],
        ["public key", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"],
        ["short ID", "0123"],
        ["PQ verify", "1"],
        ["spiderX", "/spider"],
      ],
    ],
    [
      "REALITY with an explicitly empty short ID",
      {
        type: "reality",
        fingerprint: "chrome",
        server_name: "cover.example.com",
        public_key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
        short_id: "",
      },
      [
        ["fingerprint", "chrome"],
        ["SNI", "cover.example.com"],
        ["public key", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"],
        // Kept, not dropped: the URI carries `sid=` and so does the card.
        ["short ID", ""],
      ],
    ],
  ])("reads %s", (_name, security, expected) => {
    expect(securityValues(security)).toEqual(expected);
  });

  it("drops fields the API omitted rather than printing blanks", () => {
    expect(securityValues({ type: "tls" })).toEqual([]);
  });
});
