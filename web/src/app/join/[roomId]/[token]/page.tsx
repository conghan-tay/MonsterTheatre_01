"use client";

import { useParams } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";
import { Theatre } from "@/components/Theatre";
import { Button, CountedField, ErrorNotice } from "@/components/ui";
import { ApiError, api, type RoomView } from "@/lib/api";
import { unlockAudio } from "@/lib/audio";

// The page a share link opens: /join/{roomId}/{token}.
//
// It always starts on the Enter theatre button, including after a reload.
// A browser will only play sound on a page that has been clicked, so every
// load needs a click before the premiere can be heard; and that click is
// also what joins the room. Joining again with the same cookie gives back
// the same session and uses no seat (docs/audience-session.md, flow 3).
//
// It also asks for a nickname, which the room's director sees. The page
// cannot ask the server who this browser is before entering, because the
// session cookie is not readable from here; so it remembers the last name
// typed for this room itself, and fills it in. Whatever is in the field
// when Enter theatre is pressed is the name the session then has.
const MAX_NICKNAME = 30;

export default function JoinPage() {
  const { roomId, token } = useParams<{ roomId: string; token: string }>();
  const [room, setRoom] = useState<RoomView | null>(null);
  const [nickname, setNickname] = useState("");
  const [nicknameError, setNicknameError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const remembered = `mt_nickname_${roomId}`;
  useEffect(() => {
    // After the page has loaded, not while it renders: storage belongs to
    // the browser, and the first render also happens on the server.
    const timer = setTimeout(() => {
      try {
        const last = window.localStorage.getItem(remembered);
        if (last) setNickname((typed) => typed || last);
      } catch {
        // Storage can be switched off; the field just starts empty.
      }
    }, 0);
    return () => clearTimeout(timer);
  }, [remembered]);

  async function enter(event: FormEvent) {
    event.preventDefault();
    // First, while this is still the click: see lib/audio.ts.
    unlockAudio();
    const name = nickname.trim();
    setError(null);
    if (!name) {
      setNicknameError("Enter a nickname.");
      return;
    }
    setNicknameError(null);
    setBusy(true);
    try {
      const entered = await api.join(roomId, token, name);
      try {
        window.localStorage.setItem(remembered, name);
      } catch {
        // Not remembered, then; nothing else depends on it.
      }
      setRoom(entered.room);
    } catch (err) {
      if (err instanceof ApiError && err.fields.nickname) setNicknameError(err.fields.nickname);
      else setError(refusal(err));
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
        <form onSubmit={enter} className="mt-8 space-y-4" noValidate>
          {error && <ErrorNotice>{error}</ErrorNotice>}
          <CountedField
            id="nickname"
            name="nickname"
            label="Nickname"
            hint="What the director will see you as."
            required
            autoComplete="nickname"
            max={MAX_NICKNAME}
            value={nickname}
            onValue={setNickname}
            error={nicknameError ?? undefined}
          />
          <Button type="submit" disabled={busy} className="w-full">
            {busy ? "Entering…" : "Enter theatre"}
          </Button>
        </form>
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
