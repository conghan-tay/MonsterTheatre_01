import { expect, test } from "@playwright/test";
import { nextResult, progressText, type Result, type TheatreState } from "../src/lib/theatre";

// Which round's result a page keeps on screen (D053). This is a pure rule,
// tested here directly: with the stand-in providers the next turn's lines
// are written in milliseconds, too fast to catch reliably in a browser.
const winner = { choiceId: "c2", text: "two", counts: { c1: 0, c2: 1, c3: 0 }, decidedBy: "majority" as const };
const choices = [
  { id: "c1", text: "one" },
  { id: "c2", text: "two" },
  { id: "c3", text: "three" },
];
const preparing: TheatreState = { phase: "preparing_line", turn: 1, speaker: "A", beat: "INTRO", choices, winner };
const held: Result = { turn: 1, speaker: "A", beat: "INTRO", winner, choices };

test("a round's result is kept when it is announced", () => {
  expect(nextResult(null, preparing)).toEqual(held);
});

test("it stays while the next turn's lines are written", () => {
  expect(nextResult(held, { phase: "generating", turn: 2, speaker: "B" })).toEqual(held);
});

test("it goes when the next round opens, or anything else happens", () => {
  expect(nextResult(held, { phase: "voting", turn: 2, speaker: "B", choices })).toBeNull();
  expect(nextResult(held, { phase: "failed", turn: 2 })).toBeNull();
  expect(nextResult(held, { phase: "recovering", turn: 0 })).toBeNull();
  expect(nextResult(held, { phase: "premiere", turn: 8 })).toBeNull();
});

test("it is not shown against the wrong turn", () => {
  // A browser that missed updates and is told turn 3 is being written.
  expect(nextResult(held, { phase: "generating", turn: 3, speaker: "A" })).toBeNull();
  // A browser that connects while lines are written has no result to keep.
  expect(nextResult(null, { phase: "generating", turn: 2, speaker: "B" })).toBeNull();
});

test("the last round's result stays while the videos are made", () => {
  const last: Result = { ...held, turn: 8, speaker: "B" };
  const making: TheatreState = { phase: "preparing_premiere", turn: 8, progress: { ready: 2, total: 8 } };
  expect(nextResult(last, making)).toEqual(last);
  expect(nextResult(last, { ...making, progress: { ready: 5, total: 8 } })).toEqual(last);
  // An earlier round's result is not shown against it.
  expect(nextResult(held, making)).toBeNull();
});

test("the progress before the premiere reads as a count of clips", () => {
  expect(progressText({ ready: 2, total: 8 })).toBe("2 of 8 clips have been generated…");
  expect(progressText({ ready: 0, total: 8 })).toBe("0 of 8 clips have been generated…");
  expect(progressText({ ready: 1, total: 8 })).toBe("1 of 8 clips has been generated…");
});
