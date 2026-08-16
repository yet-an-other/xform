import { useState } from "react";

import { login } from "@/lib/api";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (await login(password)) {
        onLogin();
      } else {
        setError("Wrong password");
      }
    } catch {
      setError("Panel unreachable");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="mx-auto flex min-h-screen w-[min(420px,calc(100%-40px))] flex-col justify-center">
      <p className="text-primary mb-2 font-mono text-xs font-bold tracking-[0.16em] uppercase">
        xform panel
      </p>
      <h1 className="mb-8 text-[clamp(2rem,6vw,3rem)] leading-none font-semibold tracking-[-0.055em]">
        Log in
      </h1>

      <form
        onSubmit={(event) => void submit(event)}
        className="bg-surface/80 flex flex-col gap-4 rounded-xl border p-6"
      >
        <label htmlFor="password" className="text-muted-foreground font-mono text-xs">
          Password
        </label>
        <input
          id="password"
          type="password"
          autoComplete="current-password"
          autoFocus
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          className="border-border bg-background text-foreground focus:border-primary rounded-lg border px-3 py-2 font-mono text-sm outline-none"
        />

        {error ? (
          <p role="alert" className="text-destructive-foreground text-sm">
            {error}
          </p>
        ) : null}

        <button
          type="submit"
          disabled={busy}
          className="bg-primary text-primary-foreground hover:opacity-90 rounded-lg px-3 py-2 text-sm font-bold tracking-[0.08em] uppercase disabled:opacity-50"
        >
          {busy ? "Logging in…" : "Log in"}
        </button>
      </form>
    </main>
  );
}
