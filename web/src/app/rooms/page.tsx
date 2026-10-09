"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ErrorNotice } from "@/components/ui";
import { ApiError, api, type RoomSummary } from "@/lib/api";

export default function RoomsPage() {
  const [rooms, setRooms] = useState<RoomSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .listRooms()
      .then((r) => !cancelled && setRooms(r))
      .catch((err) => !cancelled && setError(err instanceof ApiError ? err.message : "Could not load your rooms."));
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">Your rooms</h1>
        <Link
          href="/rooms/new"
          className="rounded-md bg-violet-700 px-4 py-2 text-sm font-semibold text-white hover:bg-violet-800 focus:outline-none focus-visible:ring-2 focus-visible:ring-violet-400 focus-visible:ring-offset-2"
        >
          New room
        </Link>
      </div>

      <div className="mt-6">
        {error && <ErrorNotice>{error}</ErrorNotice>}
        {!error && rooms === null && <p className="text-stone-500">Loading…</p>}
        {rooms?.length === 0 && (
          <p className="text-stone-600">
            You have no rooms yet. A room holds one scene, two characters, and one performance.
          </p>
        )}
        {rooms && rooms.length > 0 && (
          <ul className="divide-y divide-stone-200 rounded-md border border-stone-200 bg-white">
            {rooms.map((room) => (
              <li key={room.roomId} className="flex items-baseline justify-between gap-4 px-4 py-3">
                <Link href={`/rooms/${room.roomId}`} className="font-medium text-violet-800 underline-offset-4 hover:underline">
                  {room.topic}
                </Link>
                <time dateTime={room.createdAt} className="shrink-0 text-xs text-stone-500">
                  {new Date(room.createdAt).toLocaleString()}
                </time>
              </li>
            ))}
          </ul>
        )}
      </div>
    </>
  );
}
