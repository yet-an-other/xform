// The typed public transport and security values behind a Connection profile
// (IN-DEV-SPEC §6.2), turned into labelled pairs a card can print. They let an
// admin read what a client will actually do without parsing the URI's query
// string by eye.
import type { ConnectionSecurity, ConnectionTransport } from "@/lib/api";

// TypedValue is one such field: the label to show, and the value as sent.
export type TypedValue = readonly [label: string, value: string];

export function transportValues(transport: ConnectionTransport): TypedValue[] {
  switch (transport.type) {
    case "ws":
    case "httpupgrade":
      return defined([
        ["path", transport.path],
        ["host", transport.host],
      ]);
    case "grpc":
      return defined([
        ["service", transport.service_name],
        ["mode", transport.mode],
        ["authority", transport.authority],
      ]);
    case "xhttp":
      // XHTTP `extra` is deliberately absent: the URI canonicalizes it with
      // RFC 8785 (§6.3 rule 12), and a second serialization here could order
      // its keys differently from the `extra=` printed on the same card. The
      // URI stays the single source for that value.
      return defined([
        ["path", transport.path],
        ["host", transport.host],
        ["mode", transport.mode],
      ]);
    default:
      return [];
  }
}

export function securityValues(security: ConnectionSecurity): TypedValue[] {
  return defined([
    ["fingerprint", security.fingerprint],
    ["SNI", security.server_name],
    ...(security.type === "reality"
      ? ([
          ["public key", security.public_key],
          ["short ID", security.short_id],
          ["PQ verify", security.post_quantum_verify],
          ["spiderX", security.spider_x],
        ] as const)
      : ([
          ["ALPN", security.alpn?.join(", ")],
          ["ECH", security.ech],
          ["pins", security.certificate_pins?.join(", ")],
          ["verify name", security.verify_name],
        ] as const)),
  ]);
}

// defined drops the fields the API omitted, and keeps the ones it sent as
// empty — an explicitly empty REALITY short ID is a real setting, not a gap.
function defined(values: readonly (readonly [string, string | undefined])[]): TypedValue[] {
  return values.filter((entry): entry is TypedValue => entry[1] !== undefined);
}
