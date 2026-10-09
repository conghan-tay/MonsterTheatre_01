"use client";

import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { Button, ErrorNotice, Field, inputClass } from "@/components/ui";
import { ApiError, api } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login(username, password);
      router.replace("/rooms");
    } catch (err) {
      // The server's message already says what happened: wrong credentials,
      // or too many attempts.
      setError(err instanceof ApiError ? err.message : "Something went wrong. Try again.");
      setBusy(false);
    }
  }

  return (
    <main className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-6 py-12">
      <h1 className="text-2xl font-bold">Monster Theatre</h1>
      <p className="mt-1 text-sm text-stone-600">Directors, log in to set up a room.</p>

      <form onSubmit={submit} className="mt-8 space-y-4" noValidate>
        {error && <ErrorNotice>{error}</ErrorNotice>}
        <Field id="username" label="Username">
          <input
            id="username"
            name="username"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className={inputClass}
          />
        </Field>
        <Field id="password" label="Password">
          <input
            id="password"
            name="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={inputClass}
          />
        </Field>
        <Button type="submit" disabled={busy || !username || !password} className="w-full">
          {busy ? "Logging in…" : "Log in"}
        </Button>
      </form>
    </main>
  );
}
