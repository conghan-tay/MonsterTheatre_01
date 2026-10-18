// What the room WebSocket carries, as the server sends it
// (server/internal/performance/state.go; docs/system-design.md, section 5).

export type Phase = "waiting" | "recovering" | "generating" | "voting" | "preparing_line" | "premiere" | "failed";

export type Speaker = "A" | "B";

export type Choice = { id: string; text: string };

export type Winner = {
  choiceId: string;
  text: string;
  counts: Record<string, number>;
  decidedBy: "majority" | "tie" | "no_votes";
};

export type Line = { turn: number; speaker: Speaker; text: string };

// url is a path on this origin; the page plays it as given (D037).
export type Clip = { turn: number; speaker: Speaker; url: string };

export type Premiere = { startsAt: string; clips: Clip[] };

export type Failure = { code: string; message: string };

// Every snapshot and update carries the whole state (D036). A field is
// absent in the phases that do not have it.
export type TheatreState = {
  phase: Phase;
  turn: number;
  performanceId?: string;
  speaker?: Speaker;
  beat?: string;
  choices?: Choice[];
  deadline?: string;
  winner?: Winner;
  // Only in a snapshot: the choice this browser's voter has made this turn.
  yourVote?: string;
  lines?: Line[];
  premiere?: Premiere;
  failure?: Failure;
};

// Who is in the room's audience, in the order they joined. Only the
// director's connection is sent this (D045).
export type Audience = { count: number; nicknames: string[] };

export type ServerMessage =
  | { type: "snapshot" | "update"; revision: number; state: TheatreState }
  | ({ type: "audience" } & Audience)
  | { type: "vote_received"; turn: number; choiceId: string }
  | { type: "error"; code: string; message: string };

export const TURNS = 8;

// A round's result, kept on screen after its phase has passed.
export type Result = { turn: number; speaker: Speaker; beat?: string; winner: Winner; choices: Choice[] };

// nextResult is the result a page shows given the one it holds and the state
// it has just been told. A winning line is tagged and voiced in the
// background, so the next turn's lines are written straight after it is
// announced; the result stays up while they are, and goes when the next
// round opens (docs/decisions.md, D053). Only a browser that was told the
// result keeps it: the next turn's state does not carry it.
export function nextResult(held: Result | null, state: TheatreState): Result | null {
  if (state.phase === "preparing_line" && state.speaker && state.winner) {
    return { turn: state.turn, speaker: state.speaker, beat: state.beat, winner: state.winner, choices: state.choices ?? [] };
  }
  if (state.phase === "generating" && held && state.turn === held.turn + 1) return held;
  return null;
}
