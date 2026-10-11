import { expect, test } from "@playwright/test";
import { scriptToShow } from "../src/lib/script";
import type { Line } from "../src/lib/theatre";

// Which lines of the script a page shows, and in what order. This is a pure
// rule, tested here directly: with the stand-in recordings a clip lasts a
// quarter of a second, too short to catch reliably in a browser.
const lines: Line[] = [
  { turn: 1, speaker: "A", text: "one" },
  { turn: 2, speaker: "B", text: "two" },
  { turn: 3, speaker: "A", text: "three" },
];
const turns = (shown: Line[]) => shown.map((line) => line.turn);

test("while the turns run, the newest line is on top", () => {
  for (const phase of ["generating", "voting", "preparing_line", "failed", "recovering"] as const) {
    expect(turns(scriptToShow(lines, phase, null)), phase).toEqual([3, 2, 1]);
  }
});

test("while a recording plays, only its line is shown", () => {
  expect(turns(scriptToShow(lines, "premiere", 2))).toEqual([2]);
  expect(turns(scriptToShow(lines, "premiere", 1))).toEqual([1]);
});

test("at the premiere with nothing playing, the whole script is in reading order", () => {
  expect(turns(scriptToShow(lines, "premiere", null))).toEqual([1, 2, 3]);
});

test("the lines it was given are left as they were", () => {
  scriptToShow(lines, "voting", null);
  expect(turns(lines)).toEqual([1, 2, 3]);
});
