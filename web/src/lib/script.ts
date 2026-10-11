import type { Line, Phase } from "./theatre";

// scriptToShow decides which of the winning lines a page lists, and in what
// order.
//
//   - While the turns run, all of them, newest first: the line just chosen
//     is the one people are looking for.
//   - At the premiere, while a recording plays, only the line being spoken.
//   - At the premiere with nothing playing (the countdown, a finished room
//     before it is played, and after the last line), all of them in the
//     order they are spoken, so the script can be read.
//
// playingTurn is the turn of the clip being heard, or null.
export function scriptToShow(lines: Line[], phase: Phase, playingTurn: number | null): Line[] {
  if (phase !== "premiere") return [...lines].reverse();
  if (playingTurn !== null) return lines.filter((line) => line.turn === playingTurn);
  return lines;
}
