// A Collection is one request for data a dialog shows, plus what the browser
// keeps of it while that dialog is open (CONTEXT.md). ADR-0006 writes the rule
// this module exists to enforce in one place: a closed dialog aborts its
// request and discards what it collected, so every opening is an initial load.

import { useCallback, useEffect, useRef, useState } from "react";

import { snapshotFailureReason, UnauthenticatedError } from "@/lib/api";

// Collection is what a dialog reads while it is open. `refreshFailed` names
// the difference between "nothing ever arrived" and "something arrived, then a
// later attempt failed" — the second still has something true to show, and
// says so beside it rather than blanking the dialog.
export interface Collection<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  refreshFailed: boolean;
  refresh: () => void;
}

export interface CollectionOptions {
  // onExpired is called instead of surfacing a 401 as an error: an expired
  // Session is the Dashboard's business, not a dialog's (SPEC §6). The collection
  // stops for good — no error, no retry, no further ticks.
  onExpired: () => void;
  // intervalMs re-collects on a cadence, for data that keeps moving while the
  // dialog is open. Operational snapshots leave it unset: they are collected
  // when an admin asks and never on a timer (ADR-0006).
  intervalMs?: number;
}

interface State<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
}

// A fresh opening holds nothing and is already collecting. One shared value,
// so the reset on a new `collect` costs no render on the first mount.
const INITIAL = { data: null, error: null, loading: true } as const;

export function useCollection<T>(
  collect: (signal: AbortSignal) => Promise<T>,
  { onExpired, intervalMs }: CollectionOptions,
): Collection<T> {
  const [state, setState] = useState<State<T>>(INITIAL);
  // onExpired lives in a ref so `collect` is the only thing whose identity can
  // start a collection over. A caller writing this callback inline must not be
  // able to re-collect on every render.
  const expired = useRef(onExpired);
  // start is the running collection's own starter, so Refresh and the timer
  // drive the same run without either being rebuilt.
  const start = useRef<(supersede: boolean) => void>(() => {});

  useEffect(() => {
    expired.current = onExpired;
  });

  useEffect(() => {
    // Everything below belongs to this `collect`. A different one is a
    // different opening: the old collection is aborted, and nothing it returns
    // afterwards reaches the screen.
    let live = true;
    // The latest collection's controller, kept after it settles: closing the
    // dialog aborts what it started, whether or not that had finished.
    let controller: AbortController | null = null;
    let inFlight = false;
    let timer: number | null = null;

    function stop() {
      live = false;
      if (timer !== null) window.clearInterval(timer);
      controller?.abort();
    }

    function run(supersede: boolean) {
      if (!live) return;
      if (inFlight) {
        // A tick means "keep it fresh" and leaves a slow collection alone —
        // restarting on every tick would starve a request slower than the
        // interval, which would then never finish. Refresh means "now, again",
        // and takes over.
        if (!supersede) return;
        controller?.abort();
      }
      const current = new AbortController();
      controller = current;
      inFlight = true;
      setState((previous) => (previous.loading ? previous : { ...previous, loading: true }));

      void (async () => {
        try {
          const value = await collect(current.signal);
          if (!live || current.signal.aborted) return;
          inFlight = false;
          setState({ data: value, error: null, loading: false });
        } catch (cause) {
          if (!live || current.signal.aborted) return;
          inFlight = false;
          if (cause instanceof UnauthenticatedError) {
            stop();
            expired.current();
            return;
          }
          // The last value stays put: a failed refresh keeps showing what it
          // has, with the failure named next to it.
          setState((previous) => ({
            ...previous,
            error: snapshotFailureReason(cause),
            loading: false,
          }));
        }
      })();
    }

    setState(INITIAL);
    start.current = run;
    run(true);
    if (intervalMs !== undefined) {
      timer = window.setInterval(() => run(false), intervalMs);
    }
    return stop;
  }, [collect, intervalMs]);

  const refresh = useCallback(() => start.current(true), []);

  return {
    ...state,
    // A failure with a value behind it is a failed refresh, not an outage.
    refreshFailed: state.data !== null && state.error !== null,
    refresh,
  };
}
