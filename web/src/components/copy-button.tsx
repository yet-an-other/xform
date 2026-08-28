import { useEffect, useState } from "react";

import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/utils";

interface CopyButtonProps {
  // value is written to the clipboard exactly as given — the same string the
  // card displays beside this action.
  value: string;
  // label names the action for assistive technology and hover, e.g. "Copy
  // connection URI for Primary". Every card's actions carry their profile
  // name, so no two read alike.
  label: string;
  className?: string;
}

const FEEDBACK_MS = 1_500;

type Outcome = "idle" | "copied" | "failed";

// What the action says once it has run. The accessible name carries the
// outcome too: the visible word changes, and a screen reader would otherwise
// hear only the static label.
const FEEDBACK: Record<Outcome, { word: string; nameSuffix: string }> = {
  idle: { word: "Copy", nameSuffix: "" },
  copied: { word: "Copied", nameSuffix: ", copied" },
  failed: { word: "Copy failed", nameSuffix: ", copy failed" },
};

// CopyButton is the small uppercase Copy action beside a copyable field. It
// confirms in place and returns to rest, so an admin copying several profiles
// can see which one landed.
export function CopyButton({ value, label, className }: CopyButtonProps) {
  const [outcome, setOutcome] = useState<Outcome>("idle");

  useEffect(() => {
    if (outcome === "idle") return;
    const timer = window.setTimeout(() => setOutcome("idle"), FEEDBACK_MS);
    return () => window.clearTimeout(timer);
  }, [outcome]);

  const feedback = FEEDBACK[outcome];

  return (
    <button
      type="button"
      aria-label={`${label}${feedback.nameSuffix}`}
      title={label}
      onClick={() => void copyText(value).then((copied) => setOutcome(copied ? "copied" : "failed"))}
      className={cn(
        "cursor-pointer text-[9px] font-extrabold tracking-[0.06em] uppercase",
        outcome === "failed" ? "text-destructive" : "text-primary",
        className,
      )}
    >
      {feedback.word}
    </button>
  );
}
