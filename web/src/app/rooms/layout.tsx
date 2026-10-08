"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState, type ReactNode } from "react";
import { ApiError, api, type Director } from "@/lib/api";

// Everything under /rooms is for a logged-in director. This layout checks
// who is logged in once, sends anyone else to /login, and draws the header.
// The API enforces access on every request; this only decides what to show.
export default function RoomsLayout({ children }: { children: ReactNode }) {
  const router = useRouter();
  const [director, setDirector] = useState<Director | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .me()
      .then((d) => !cancelled && setDirector(d))
      .catch((err) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 401) {
          router.replace("/login");
        } else {
          setProblem(err instanceof ApiError ? err.message : "Something went wrong.");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [router]);

  async function logOut() {
    await api.logout().catch(() => undefined);
    router.replace("/login");
  }

  if (problem) {
    return (
      <main className="mx-auto max-w-3xl px-6 py-12">
        <p role="alert" className="text-red-800">
          {problem}
        </p>
      </main>
    );
  }
  if (!director) {
    return (
      <main className="mx-auto max-w-3xl px-6 py-12 text-stone-500" aria-busy="true">
        Loading…
      </main>
    );
  }

  return (
    <div className="mx-auto max-w-3xl px-6">
      <header className="flex items-center justify-between border-b border-stone-200 py-4">
        <nav className="flex items-baseline gap-6">
          <span className="font-bold">Monster Theatre</span>
          <Link href="/rooms" className="text-sm text-violet-800 underline-offset-4 hover:underline">
            Your rooms
          </Link>
        </nav>
        <div className="flex items-baseline gap-4 text-sm">
          <span className="text-stone-600">{director.username}</span>
          <button onClick={logOut} className="text-violet-800 underline-offset-4 hover:underline">
            Log out
          </button>
        </div>
      </header>
      <main className="py-8">{children}</main>
    </div>
  );
}
