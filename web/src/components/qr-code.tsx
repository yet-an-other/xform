import { useMemo } from "react";

import { encodeQr, qrPathData, QUIET_ZONE_MODULES } from "@/lib/qr";
import { cn } from "@/lib/utils";

interface QrCodeProps {
  // value is encoded as its own UTF-8 bytes — for a Connection profile, the
  // canonical URI exactly as displayed and copied (IN-DEV-SPEC §6.3). There
  // is no wrapper, no re-serialization, and no trimming between the two.
  value: string;
  label: string;
  className?: string;
}

export function QrCode({ value, label, className }: QrCodeProps) {
  const symbol = useMemo(() => {
    try {
      const code = encodeQr(new TextEncoder().encode(value));
      return { size: code.size, path: qrPathData(code) };
    } catch {
      // Only a URI beyond every QR version's capacity lands here; the card
      // keeps its copyable text rather than taking the modal down.
      return null;
    }
  }, [value]);

  if (symbol === null) {
    return (
      <p className={cn("text-muted-foreground text-[10px]", className)}>
        This URI is too long for a QR code — copy it instead.
      </p>
    );
  }

  // Drawing the quiet zone inside the viewBox keeps the symbol scannable
  // whatever the card puts behind it.
  const span = symbol.size + QUIET_ZONE_MODULES * 2;
  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`${-QUIET_ZONE_MODULES} ${-QUIET_ZONE_MODULES} ${span} ${span}`}
      shapeRendering="crispEdges"
      className={cn("fill-qr-ink block", className)}
    >
      <path d={symbol.path} />
    </svg>
  );
}
