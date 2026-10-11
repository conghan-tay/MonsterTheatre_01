"use client";

import { useParams } from "next/navigation";
import { useState } from "react";
import { Theatre } from "@/components/Theatre";
import { Button, ErrorNotice } from "@/components/ui";
import { ApiError, api, type RoomView } from "@/lib/api";
import { unlockAudio } from "@/lib/audio";

// The page a share link opens: /join/{roomId}/{token}.
//
// It always starts on the Enter theatre button, including after a reload.
// A browser will only play sound on a page that has been clicked, so every
// load needs a click before the premiere can be heard; and that click is
// also what joins the room. Joining again with the same cookie gives back
// the same session and uses no seat (docs/audience-session.md, flow 3).
export default function JoinPage() {
  const { roomId, token } = useParams<{ roomId: string; token: string }>();
  const [room, setRoom] = useState<RoomView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function enter() {
    // First, while this is still the click: see lib/audio.ts.
    unlockAudio();
    setBusy(true);
    setError(null);
    try {
      const entered = await api.join(roomId, token);
      setRoom(entered.room);
    } catch (err) {
      setError(refusal(err));
      setBusy(false);
    }
  }

  if (!room) {
    return (
      <main className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-6 py-12">
        <h1 className="text-2xl font-bold">Monster Theatre</h1>
        <p className="mt-1 text-sm text-stone-600">
          You have been invited to a performance. You will vote on each line, then hear the result.
        </p>
        <div className="mt-8 space-y-4">
          {error && <ErrorNotice>{error}</ErrorNotice>}
          <Button type="button" onClick={enter} disabled={busy} className="w-full">
            {busy ? "Entering…" : "Enter theatre"}
          </Button>
        </div>
      </main>
    );
  }

  return (
    <main className="mx-auto max-w-3xl space-y-8 px-6 py-8">
      <RoomHeading room={room} />
      <Theatre
        room={room}
        beforeStart={<p className="text-stone-700">Waiting for the director to start the performance.</p>}
      />
    </main>
  );
}

function RoomHeading({ room }: { room: RoomView }) {
  return (
    <div>
      <h1 className="text-2xl font-bold">{room.scene.topic}</h1>
      {room.scene.genre && <p className="mt-1 text-sm uppercase tracking-wide text-stone-500">{room.scene.genre}</p>}
      {room.scene.description && <p className="mt-3 text-stone-700">{room.scene.description}</p>}
    </div>
  );
}

function refusal(err: unknown): string {
  if (!(err instanceof ApiError)) return "Something went wrong. Try again.";
  if (err.status === 403) return "This link is not valid.";
  if (err.status === 409) return "This theatre is full.";
  return err.message;
}
