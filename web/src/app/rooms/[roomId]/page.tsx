"use client";

import { useParams, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { Button, ErrorNotice } from "@/components/ui";
import { ApiError, api, type Character, type Room } from "@/lib/api";

const MAX_AUDIENCE = 10;

export default function RoomPage() {
  const { roomId } = useParams<{ roomId: string }>();
  const router = useRouter();
  const [room, setRoom] = useState<Room | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

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

      <section aria-label="Characters" className="grid gap-6 sm:grid-cols-2">
        <CharacterCard slot="A" character={room.characterA} />
        <CharacterCard slot="B" character={room.characterB} />
      </section>

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
        <p className="text-sm text-stone-600" aria-live="polite">
          {room.audienceCount} of {MAX_AUDIENCE} joined
        </p>
      </section>

      <section className="space-y-2">
        <Button type="button" disabled aria-describedby="start-note">
          Start performance
        </Button>
        <p id="start-note" className="text-sm text-stone-500">
          Starting a performance is not built yet. It arrives in a later step.
        </p>
      </section>
    </div>
  );
}

function CharacterCard({ slot, character }: { slot: string; character: Character }) {
  return (
    <article className="flex gap-4 rounded-md border border-stone-200 bg-white p-4">
      {/* A plain img: the portrait is private, served by the API with the
          session cookie, and already the size it was uploaded at. */}
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={character.portraitUrl}
        alt={`Portrait of ${character.name}`}
        width={96}
        height={96}
        className="h-24 w-24 shrink-0 rounded-md bg-stone-100 object-cover"
      />
      <div>
        <p className="text-xs uppercase tracking-wide text-stone-500">Character {slot}</p>
        <h2 className="text-lg font-semibold">{character.name}</h2>
        {character.brief && <p className="mt-1 text-sm text-stone-700">{character.brief}</p>}
      </div>
    </article>
  );
}
