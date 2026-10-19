"use client";

import { useParams, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { Theatre } from "@/components/Theatre";
import { Button, ErrorNotice } from "@/components/ui";
import { ApiError, api, type Room } from "@/lib/api";
import { unlockAudio } from "@/lib/audio";
import type { Audience } from "@/lib/theatre";

const MAX_AUDIENCE = 10;

export default function RoomPage() {
  const { roomId } = useParams<{ roomId: string }>();
  const router = useRouter();
  const [room, setRoom] = useState<Room | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  // Who has joined, as the room's WebSocket says. Until it has spoken, the
  // count that came with the room is shown.
  const [audience, setAudience] = useState<Audience | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .getRoom(roomId)
      .then((r) => !cancelled && setRoom(r))
      .catch((err) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 401) return router.replace("/login");
        setError(err instanceof ApiError ? err.message : "Could not load this room.");
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, router]);

  async function copyLink() {
    if (!room) return;
    try {
      await navigator.clipboard.writeText(room.shareLink);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access can be refused; the link is in the field to copy by hand.
    }
  }

  if (error) return <ErrorNotice>{error}</ErrorNotice>;
  if (!room) return <p className="text-stone-500">Loading…</p>;

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold">{room.scene.topic}</h1>
        {room.scene.genre && <p className="mt-1 text-sm uppercase tracking-wide text-stone-500">{room.scene.genre}</p>}
        {room.scene.description && <p className="mt-3 text-stone-700">{room.scene.description}</p>}
      </div>

      <section className="space-y-2 rounded-md border border-stone-200 bg-white p-4">
        <label htmlFor="share-link" className="block text-sm font-semibold">
          Share link
        </label>
        <p className="text-sm text-stone-600">Send this to your audience. Anyone with the link can join and vote.</p>
        <div className="flex gap-2">
          <input
            id="share-link"
            readOnly
            value={room.shareLink}
            onFocus={(e) => e.currentTarget.select()}
            className="block w-full rounded-md border border-stone-300 bg-stone-50 px-3 py-2 font-mono text-sm text-stone-800"
          />
          <Button type="button" onClick={copyLink} className="shrink-0">
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
        <div aria-live="polite" className="space-y-2">
          <p className="text-sm text-stone-600">
            {audience?.count ?? room.audienceCount} of {MAX_AUDIENCE} joined
          </p>
          {audience && audience.nicknames.length > 0 && (
            <ul aria-label="Audience" className="flex flex-wrap gap-2">
              {audience.nicknames.map((nickname, i) => (
                // Two people may use the same name, so the place in the list is the key.
                <li key={i} className="rounded-full border border-stone-300 bg-stone-50 px-3 py-1 text-sm text-stone-800">
                  {nickname}
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>

      {/* The live view, with voting: the director is one more voter (D028). */}
      <Theatre room={room} beforeStart={<StartPerformance roomId={room.roomId} />} onAudience={setAudience} />
    </div>
  );
}

// The Start button, shown while the room waits. Pressing it is safe to
// repeat. Once the performance is running the live view replaces it.
function StartPerformance({ roomId }: { roomId: string }) {
  const [busy, setBusy] = useState(false);
  // Off unless the director asks: it adds minutes before the premiere and
  // is paid for by the second (D055).
  const [lipSync, setLipSync] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function start() {
    // First, while this is still the click: it lets this page play the
    // premiere later (lib/audio.ts).
    unlockAudio();
    setBusy(true);
    setError(null);
    try {
      await api.start(roomId, lipSync);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "The performance could not be started. Press Start again.");
    }
    setBusy(false);
  }

  return (
    <div className="space-y-3">
      <p className="text-stone-700">The performance has not started.</p>
      {error && <ErrorNotice>{error}</ErrorNotice>}
      <label className="flex items-start gap-2 text-stone-700">
        <input
          type="checkbox"
          className="mt-1"
          checked={lipSync}
          onChange={(e) => setLipSync(e.target.checked)}
          disabled={busy}
          aria-describedby="lip-sync-note"
        />
        <span>Include lip-sync video</span>
      </label>
      <p id="lip-sync-note" className="-mt-2 pl-6 text-sm text-stone-500">
        Each line becomes a video of its speaker&rsquo;s portrait. It adds several minutes before the premiere and is
        paid for per second of speech.
      </p>
      <Button type="button" onClick={start} disabled={busy}>
        {busy ? "Starting…" : "Start performance"}
      </Button>
      <p className="text-sm text-stone-500">
        Once started it runs by itself: eight turns, each with a vote, then the premiere. It cannot be stopped.
      </p>
    </div>
  );
}
