import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Login } from "./login";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function fillAndSubmit(password: string) {
  fireEvent.change(screen.getByLabelText(/password/i), { target: { value: password } });
  fireEvent.click(screen.getByRole("button", { name: /log in/i }));
}

describe("login page", () => {
  it("rejects a wrong password without calling back", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response('{"error":"invalid password"}', { status: 401 })),
    );
    const onLogin = vi.fn();
    render(<Login onLogin={onLogin} />);

    fillAndSubmit("nope");

    expect(await screen.findByText(/wrong password/i)).toBeInTheDocument();
    expect(onLogin).not.toHaveBeenCalled();
  });

  it("posts the password to the relative login endpoint and calls back on 204", async () => {
    const spy = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", spy);
    const onLogin = vi.fn();
    render(<Login onLogin={onLogin} />);

    fillAndSubmit("correct-horse");

    await waitFor(() => expect(onLogin).toHaveBeenCalledTimes(1));
    expect(spy).toHaveBeenCalledWith(
      "api/v1/login",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ password: "correct-horse" }) }),
    );
  });

  it("reports an unreachable panel distinctly from a wrong password", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("fetch failed")));
    render(<Login onLogin={() => {}} />);

    fillAndSubmit("whatever");

    expect(await screen.findByText(/unreachable/i)).toBeInTheDocument();
  });
});
