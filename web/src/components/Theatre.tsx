"use client";

import { useEffect, useState, type ReactNode } from "react";
import { CharacterCard } from "@/components/CharacterCard";
import { Button, ErrorNotice } from "@/components/ui";
import type { RoomView } from "@/lib/api";
import { unlockAudio } from "@/lib/audio";
import { Stage } from "@/components/Stage";
import { scriptToShow, stageLine } from "@/lib/script";
import { TURNS, progressText, type Audience, type Choice, type Speaker, type Winner } from "@/lib/theatre";
import { usePremiere } from "@/lib/usePremiere";
import { useRoomSocket } from "@/lib/useRoomSocket";

// Theatre is the live view of a room, the same for its director and its
// audience: the two characters, what is happening now, and the script so
// far. It opens the room's WebSocket and shows whatever that says.
//
// beforeStart is what to show while the room waits for its performance: the
// Start button for the director, a line of text for everyone else.
//
// onAudience is called with who has joined, each time the server says. Only
// the director's page is told, so only it passes one.
export function Theatre({
  room,
  beforeStart,
  onAudience,
}: {
  room: RoomView;
  beforeStart: ReactNode;
  onAudience?: (audience: Audience) => void;
}) {
  const { state, result, connection, votes, vote, voteError, audience } = useRoomSocket(room.roomId);
  const premiere = usePremiere(state?.phase === "premiere" ? state.premiere : undefined);

  useEffect(() => {
    if (audience) onAudience?.(audience);
  }, [audience, onAudience]);

  const lines = state?.lines ? scriptToShow(state.lines, state.phase, premiere.current?.turn ?? null) : [];

  const name = (speaker: Speaker) => (speaker === "A" ? room.characterA.name : room.characterB.name);
  // Whose line it is: the clip being heard, or the turn being decided.
  const speaking = premiere.current?.speaker ?? (state?.phase === "premiere" ? undefined : state?.speaker);

  // Every button that starts playback does it inside its own click, which
  // is what lets a browser that blocked the premiere play it now.
  const playFromClick = () => {
    unlockAudio();
    void premiere.play();
  };

  // While a line is heard at the premiere, its speaker takes the stage.
  const staged = state?.phase === "premiere" && premiere.status === "playing" ? premiere.current : null;
  const stagedCharacter = staged ? (staged.speaker === "A" ? room.characterA : room.characterB) : null;

  return (
    <div className="space-y-8">
      {staged && stagedCharacter && (
        <Stage
          turn={staged.turn}
          name={stagedCharacter.name}
          portraitUrl={stagedCharacter.portraitUrl}
          videoUrl={premiere.video}
          line={stageLine(state?.lines ?? [], staged.turn)}
        />
      )}
      <section aria-label="Characters" className="grid gap-6 sm:grid-cols-2">
        <CharacterCard slot="A" character={room.characterA} speaking={speaking === "A"} />
        <CharacterCard slot="B" character={room.characterB} speaking={speaking === "B"} />
      </section>

      {connection === "reconnecting" && (
        <p role="status" className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900">
          Reconnecting…
        </p>
      )}

      <section aria-label="Now" aria-live="polite" className="rounded-md border border-stone-200 bg-white p-4">
        {!state && <p className="text-stone-500">Connecting…</p>}

        {state?.phase === "waiting" && beforeStart}

        {state?.phase === "recovering" && (
          <p className="text-stone-700">The performance is recovering. It will carry on shortly.</p>
        )}

        {state?.phase === "generating" && state.speaker && (
          <>
            {result && (
              <div className="mb-6">
                <TurnHeading turn={result.turn} name={name(result.speaker)} beat={result.beat} />
                <ChosenLine winner={result.winner} choices={result.choices} />
              </div>
            )}
            <TurnHeading turn={state.turn} name={name(state.speaker)} beat={state.beat} />
            <p className="mt-2 text-stone-600">Writing the next lines…</p>
          </>
        )}

        {state?.phase === "voting" && state.speaker && (
          <>
            <TurnHeading turn={state.turn} name={name(state.speaker)} beat={state.beat} />
            <Voting
              choices={state.choices ?? []}
              deadline={state.deadline}
              voted={votes[state.turn]}
              onVote={(choiceId) => vote(state.turn, choiceId)}
            />
            {voteError && <p className="mt-2 text-sm text-red-700">{voteError}</p>}
          </>
        )}

        {state?.phase === "preparing_line" && state.speaker && state.winner && (
          <>
            <TurnHeading turn={state.turn} name={name(state.speaker)} beat={state.beat} />
            <ChosenLine winner={state.winner} choices={state.choices ?? []} />
            <p className="mt-3 text-stone-600">Preparing the next line…</p>
          </>
        )}

        {state?.phase === "preparing_premiere" && state.progress && (
          <>
            {result && (
              <div className="mb-6">
                <TurnHeading turn={result.turn} name={name(result.speaker)} beat={result.beat} />
                <ChosenLine winner={result.winner} choices={result.choices} />
              </div>
            )}
            <p className="text-stone-700">{progressText(state.progress)}</p>
            <p className="mt-1 text-sm text-stone-500">The premiere starts when every clip is ready.</p>
          </>
        )}

        {state?.phase === "premiere" && (
          <div data-testid="premiere" data-played={premiere.played} className="space-y-3">
            {premiere.status === null && <p className="text-stone-500">Loading the performance…</p>}
            {premiere.status === "countdown" && (
              <p className="text-lg font-semibold">
                The premiere starts in <span className="tabular-nums">{premiere.secondsLeft}</span>
              </p>
            )}
            {premiere.status === "ready" && (
              <>
                <p className="text-stone-700">This performance has had its premiere. You can play it from the beginning.</p>
                <Button type="button" onClick={playFromClick}>
                  Play performance
                </Button>
              </>
            )}
            {premiere.status === "playing" && premiere.current && (
              <p className="text-lg font-semibold">
                Now playing line {premiere.current.turn} of {TURNS}: {name(premiere.current.speaker)}
              </p>
            )}
            {premiere.status === "blocked" && (
              <>
                <p className="text-stone-700">Your browser needs a click before it will play sound.</p>
                <Button type="button" onClick={playFromClick}>
                  Tap to hear audio
                </Button>
              </>
            )}
            {premiere.status === "finished" && (
              <>
                <p className="text-stone-700">The performance has finished.</p>
                <Button type="button" onClick={playFromClick}>
                  Play again
                </Button>
              </>
            )}
            {premiere.status === "error" && (
              <>
                <ErrorNotice>A recording could not be played.</ErrorNotice>
                <Button type="button" onClick={playFromClick}>
                  Try again
                </Button>
              </>
            )}
          </div>
        )}

        {state?.phase === "failed" && (
          <ErrorNotice>
            {state.failure?.message ?? "The performance could not continue."} This performance has ended; another one
            needs a new room.
          </ErrorNotice>
        )}
      </section>

      {lines.length > 0 && (
        <section className="space-y-2">
          <h2 className="text-sm font-semibold uppercase tracking-wide text-stone-500">
            Script
            {state?.phase !== "premiere" && <span className="font-normal normal-case tracking-normal">, newest first</span>}
          </h2>
          <ol aria-label="Script" className="space-y-2">
            {lines.map((line) => (
              <li
                key={line.turn}
                className={
                  "rounded-md border px-3 py-2 " +
                  (premiere.current?.turn === line.turn ? "border-violet-600 bg-violet-50" : "border-stone-200 bg-white")
                }
              >
                <span className="font-semibold">{name(line.speaker)}:</span> {line.text}
              </li>
            ))}
          </ol>
        </section>
      )}
    </div>
  );
}

function TurnHeading({ turn, name, beat }: { turn: number; name: string; beat?: string }) {
  return (
    <div className="flex items-baseline justify-between gap-4">
      <h2 className="text-lg font-semibold">
        Turn {turn} of {TURNS}: {name}&rsquo;s line
      </h2>
      {beat && <span className="text-xs uppercase tracking-wide text-stone-500">{beat}</span>}
    </div>
  );
}

// The three lines to choose between, and how long is left. A voter's first
// vote stands, so once one is cast the buttons stop taking clicks.
function Voting({
  choices,
  deadline,
  voted,
  onVote,
}: {
  choices: Choice[];
  deadline?: string;
  voted?: string;
  onVote: (choiceId: string) => void;
}) {
  return (
    <div className="mt-3 space-y-3">
      <div className="flex items-baseline justify-between gap-4 text-sm text-stone-600">
        <p>{voted ? "Your vote is in." : "Choose the line you want to hear."}</p>
        {deadline && <Countdown until={deadline} />}
      </div>
      <div role="group" aria-label="Choices" className="grid gap-2">
        {choices.map((choice) => (
          <button
            key={choice.id}
            type="button"
            aria-pressed={voted === choice.id}
            disabled={voted !== undefined}
            onClick={() => onVote(choice.id)}
            className={
              "rounded-md border px-4 py-3 text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-violet-400 " +
              (voted === choice.id
                ? "border-violet-700 bg-violet-700 text-white"
                : "border-stone-300 bg-white hover:border-violet-600 disabled:cursor-not-allowed disabled:text-stone-400 disabled:hover:border-stone-300")
            }
          >
            {choice.text}
          </button>
        ))}
      </div>
    </div>
  );
}

// Seconds left until a time the server gave, by this browser's clock.
function Countdown({ until }: { until: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(timer);
  }, []);
  const left = Math.max(0, Math.ceil((Date.parse(until) - now) / 1000));
  return (
    <span role="timer" className="shrink-0 font-semibold tabular-nums text-stone-900">
      {left} s left
    </span>
  );
}

const DECIDED: Record<Winner["decidedBy"], string> = {
  majority: "It had the most votes.",
  tie: "The vote was tied, so one of the leading lines was picked at random.",
  no_votes: "Nobody voted, so a line was picked at random.",
};

function ChosenLine({ winner, choices }: { winner: Winner; choices: Choice[] }) {
  return (
    <section aria-label="The chosen line" className="mt-3 space-y-3">
      <p className="rounded-md border border-violet-600 bg-violet-50 px-4 py-3 text-lg">{winner.text}</p>
      <p className="text-sm text-stone-600">{DECIDED[winner.decidedBy]}</p>
      <ul className="space-y-1 text-sm text-stone-600">
        {choices.map((choice) => {
          const count = winner.counts[choice.id] ?? 0;
          return (
            <li key={choice.id} className="flex justify-between gap-4">
              <span>{choice.text}</span>
              <span className="shrink-0 tabular-nums">
                {count} {count === 1 ? "vote" : "votes"}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
