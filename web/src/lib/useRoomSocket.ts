"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ServerMessage, TheatreState } from "./theatre";

export type Connection = "connecting" | "open" | "reconnecting";

// How long to wait before each try at reconnecting, in seconds; the last
// one repeats (docs/audience-session.md, flow 3, case A).
const RETRY_AFTER = [1, 2, 4, 8, 10];
// How often to ask again while the performance is recovering. The server
// answers one resync a second (docs/decisions.md, D041).
const RECOVERING_RESYNC_MS = 3000;

// useRoomSocket holds the room's WebSocket open and returns what it says.
//
// The rules are the server's (docs/system-design.md, section 6): a snapshot
// replaces the state, and an update is taken only if its revision is higher
// than the one held. An update that skips a revision means one was missed;
// it is still taken, since it carries the whole state, and a resync is sent
// for the one thing an update cannot carry, this browser's own vote.
//
// Pass null to stay disconnected, for a page that has not entered yet.
export function useRoomSocket(roomId: string | null) {
  const [state, setState] = useState<TheatreState | null>(null);
  const [connection, setConnection] = useState<Connection>("connecting");
  // The choice sent or known for each turn. A vote is shown as soon as it is
  // clicked and withdrawn if the server says it could not be passed on.
  const [votes, setVotes] = useState<Record<number, string>>({});
  const [voteError, setVoteError] = useState<string | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const pendingVote = useRef<number | null>(null);

  useEffect(() => {
    if (!roomId) return;
    let stopped = false;
    let attempt = 0;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let held = -1; // the revision held; -1 before the first snapshot

    const send = (ws: WebSocket, message: object) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(message));
    };

    const connect = () => {
      const scheme = window.location.protocol === "https:" ? "wss" : "ws";
      const ws = new WebSocket(`${scheme}://${window.location.host}/api/v1/rooms/${encodeURIComponent(roomId)}/ws`);
      socket.current = ws;

      ws.onopen = () => {
        attempt = 0;
        setConnection("open");
      };

      ws.onmessage = (event) => {
        let message: ServerMessage;
        try {
          message = JSON.parse(String(event.data));
        } catch {
          return;
        }
        switch (message.type) {
          case "snapshot": {
            held = message.revision;
            const { state } = message;
            setState(state);
            if (state.yourVote) setVotes((v) => ({ ...v, [state.turn]: state.yourVote! }));
            break;
          }
          case "update":
            if (message.revision <= held) break;
            if (message.revision > held + 1) send(ws, { type: "resync" });
            held = message.revision;
            setState(message.state);
            break;
          case "vote_received":
            pendingVote.current = null;
            setVotes((v) => ({ ...v, [message.turn]: message.choiceId }));
            break;
          case "error": {
            // The only thing this page sends that can be refused is a vote.
            const turn = pendingVote.current;
            pendingVote.current = null;
            if (turn !== null) {
              setVotes((v) => {
                const rest = { ...v };
                delete rest[turn];
                return rest;
              });
              setVoteError(message.message);
            }
            break;
          }
        }
      };

      ws.onclose = () => {
        if (stopped) return;
        setConnection("reconnecting");
        const wait = RETRY_AFTER[Math.min(attempt, RETRY_AFTER.length - 1)];
        attempt += 1;
        retry = setTimeout(connect, wait * 1000);
      };
    };

    connect();
    return () => {
      stopped = true;
      clearTimeout(retry);
      socket.current?.close();
      socket.current = null;
    };
  }, [roomId]);

  // While the performance is recovering, keep asking. The next update also
  // arrives by itself once the worker is back; either one ends this.
  const recovering = state?.phase === "recovering";
  useEffect(() => {
    if (!recovering) return;
    const timer = setInterval(() => {
      const ws = socket.current;
      if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resync" }));
    }, RECOVERING_RESYNC_MS);
    return () => clearInterval(timer);
  }, [recovering]);

  const vote = useCallback((turn: number, choiceId: string) => {
    const ws = socket.current;
    if (ws?.readyState !== WebSocket.OPEN) {
      setVoteError("Your vote could not be sent. Try again.");
      return;
    }
    setVoteError(null);
    pendingVote.current = turn;
    setVotes((v) => ({ ...v, [turn]: choiceId }));
    ws.send(JSON.stringify({ type: "vote", turn, choiceId }));
  }, []);

  return { state, connection, votes, vote, voteError };
}
