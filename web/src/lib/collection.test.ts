import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { SnapshotUnavailableError, UnauthenticatedError } from "@/lib/api";
import { useCollection } from "@/lib/collection";

// The lifecycle rules the three Viewers used to hand-roll, proved once here:
// every opening is an initial load (ADR-0006), a closed dialog aborts what it
// started, a failed refresh keeps what it has, and a 401 belongs to the
// Session rather than the dialog.

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: unknown) => void;
  const promise = new Promise<T>((settle, fail) => {
    resolve = settle;
    reject = fail;
  });
  return { promise, resolve, reject };
}

const noop = () => {};

describe("a Collection's first opening", () => {
  it("collects once and settles with the value", async () => {
    const collect = vi.fn(async () => "first");
    const { result } = renderHook(() => useCollection(collect, { onExpired: noop }));

    expect(result.current.data).toBeNull();
    expect(result.current.loading).toBe(true);

    await waitFor(() => expect(result.current.data).toBe("first"));
    expect(result.current.loading).toBe(false);
    expect(result.current.error).toBeNull();
    expect(result.current.refreshFailed).toBe(false);
    expect(collect).toHaveBeenCalledTimes(1);
  });

  it("does not collect again just because the caller re-rendered", async () => {
    const collect = vi.fn(async () => "value");
    // A fresh onExpired identity on every render — the ref is what keeps that
    // from reopening the request.
    const { result, rerender } = renderHook(({ onExpired }) => useCollection(collect, { onExpired }), {
      initialProps: { onExpired: () => {} },
    });
    await waitFor(() => expect(result.current.data).toBe("value"));

    rerender({ onExpired: () => {} });
    rerender({ onExpired: () => {} });

    expect(collect).toHaveBeenCalledTimes(1);
  });

  it("starts over with nothing when the input changes", async () => {
    const second = deferred<string>();
    const collectFirst = async () => "first";
    const collectSecond = () => second.promise;
    const { result, rerender } = renderHook(
      ({ collect }: { collect: () => Promise<string> }) => useCollection(collect, { onExpired: noop }),
      { initialProps: { collect: collectFirst } },
    );
    await waitFor(() => expect(result.current.data).toBe("first"));

    rerender({ collect: collectSecond });

    expect(result.current.data).toBeNull();
    expect(result.current.loading).toBe(true);
    await act(async () => {
      second.resolve("second");
    });
    expect(result.current.data).toBe("second");
  });

  it("ignores what a superseded input returns afterwards", async () => {
    const first = deferred<string>();
    let firstSignal!: AbortSignal;
    const collectFirst = (signal: AbortSignal) => {
      firstSignal = signal;
      return first.promise;
    };
    const collectSecond = async () => "second";
    const { result, rerender } = renderHook(
      ({ collect }: { collect: (signal: AbortSignal) => Promise<string> }) =>
        useCollection(collect, { onExpired: noop }),
      { initialProps: { collect: collectFirst } },
    );

    rerender({ collect: collectSecond });
    await waitFor(() => expect(result.current.data).toBe("second"));

    expect(firstSignal.aborted).toBe(true);
    await act(async () => {
      first.resolve("first");
    });
    expect(result.current.data).toBe("second");
  });
});

describe("asking again", () => {
  it("supersedes an in-flight collection when Refresh asks", async () => {
    const pending = [deferred<string>(), deferred<string>()];
    const signals: AbortSignal[] = [];
    const collect = vi.fn((signal: AbortSignal) => {
      signals.push(signal);
      return pending[signals.length - 1].promise;
    });
    const { result } = renderHook(() => useCollection(collect, { onExpired: noop }));
    await waitFor(() => expect(collect).toHaveBeenCalledTimes(1));

    act(() => result.current.refresh());

    expect(collect).toHaveBeenCalledTimes(2);
    expect(signals[0].aborted).toBe(true);
    await act(async () => {
      pending[1].resolve("second");
    });
    expect(result.current.data).toBe("second");

    // The superseded collection lands nowhere, however late it answers.
    await act(async () => {
      pending[0].resolve("first");
    });
    expect(result.current.data).toBe("second");
  });

  it("re-collects on the cadence", async () => {
    vi.useFakeTimers();
    try {
      let count = 0;
      const collect = vi.fn(async () => `value ${++count}`);
      const { result } = renderHook(() =>
        useCollection(collect, { onExpired: noop, intervalMs: 5_000 }),
      );
      await act(async () => {});
      expect(result.current.data).toBe("value 1");

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(collect).toHaveBeenCalledTimes(2);
      expect(result.current.data).toBe("value 2");
    } finally {
      vi.useRealTimers();
    }
  });

  // A tick means "keep it fresh". Restarting on every tick would starve a
  // collection slower than the cadence, which would then never finish.
  it("lets ticks pass rather than restarting a collection slower than the cadence", async () => {
    vi.useFakeTimers();
    try {
      const slow = deferred<string>();
      const collect = vi.fn(() => slow.promise);
      renderHook(() => useCollection(collect, { onExpired: noop, intervalMs: 5_000 }));

      await act(async () => {
        await vi.advanceTimersByTimeAsync(15_000);
      });
      expect(collect).toHaveBeenCalledTimes(1);

      await act(async () => {
        slow.resolve("late");
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(collect).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("a collection that fails", () => {
  it("keeps the last value and names the failure a failed refresh", async () => {
    let attempt = 0;
    const collect = vi.fn(async () => {
      attempt += 1;
      if (attempt === 1) return "kept";
      throw new SnapshotUnavailableError("journal unavailable");
    });
    const { result } = renderHook(() => useCollection(collect, { onExpired: noop }));
    await waitFor(() => expect(result.current.data).toBe("kept"));

    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.error).toBe("journal unavailable"));

    expect(result.current.data).toBe("kept");
    expect(result.current.refreshFailed).toBe(true);
    expect(result.current.loading).toBe(false);
  });

  it("clears the failure once a later collection succeeds", async () => {
    let attempt = 0;
    const collect = vi.fn(async () => {
      attempt += 1;
      if (attempt === 2) throw new SnapshotUnavailableError("temporarily unavailable");
      return `value ${attempt}`;
    });
    const { result } = renderHook(() => useCollection(collect, { onExpired: noop }));
    await waitFor(() => expect(result.current.data).toBe("value 1"));
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.refreshFailed).toBe(true));

    act(() => result.current.refresh());

    await waitFor(() => expect(result.current.data).toBe("value 3"));
    expect(result.current.error).toBeNull();
    expect(result.current.refreshFailed).toBe(false);
  });

  it("reports a first failure as an outage, with nothing behind it", async () => {
    const collect = vi.fn(async (): Promise<string> => {
      throw new SnapshotUnavailableError("unit not found");
    });
    const { result } = renderHook(() => useCollection(collect, { onExpired: noop }));

    await waitFor(() => expect(result.current.error).toBe("unit not found"));
    expect(result.current.data).toBeNull();
    expect(result.current.refreshFailed).toBe(false);
    expect(result.current.loading).toBe(false);
  });

  it("hands a 401 to the Session and stops for good, surfacing no error", async () => {
    vi.useFakeTimers();
    try {
      const onExpired = vi.fn();
      const collect = vi.fn(async (): Promise<string> => {
        throw new UnauthenticatedError();
      });
      const { result } = renderHook(() => useCollection(collect, { onExpired, intervalMs: 5_000 }));

      await act(async () => {});

      expect(onExpired).toHaveBeenCalledTimes(1);
      expect(result.current.error).toBeNull();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(20_000);
      });
      expect(collect).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("closing the dialog", () => {
  it("aborts the collection it started and keeps its answer off the screen", async () => {
    const pending = deferred<string>();
    let signal!: AbortSignal;
    const collect = (current: AbortSignal) => {
      signal = current;
      return pending.promise;
    };
    const { result, unmount } = renderHook(() => useCollection(collect, { onExpired: noop }));

    unmount();

    expect(signal.aborted).toBe(true);
    await act(async () => {
      pending.resolve("late");
    });
    expect(result.current.data).toBeNull();
  });

  // Aborting a collection that already answered is what makes reopening an
  // initial load rather than a resumption (ADR-0006).
  it("aborts even after the collection finished", async () => {
    let signal!: AbortSignal;
    const collect = async (current: AbortSignal) => {
      signal = current;
      return "collected";
    };
    const { result, unmount } = renderHook(() => useCollection(collect, { onExpired: noop }));
    await waitFor(() => expect(result.current.data).toBe("collected"));

    unmount();

    expect(signal.aborted).toBe(true);
  });

  it("stops the cadence", async () => {
    vi.useFakeTimers();
    try {
      const collect = vi.fn(async () => "value");
      const { unmount } = renderHook(() =>
        useCollection(collect, { onExpired: noop, intervalMs: 5_000 }),
      );
      await act(async () => {});

      unmount();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(20_000);
      });
      expect(collect).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
