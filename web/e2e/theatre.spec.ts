import fs from "node:fs";
import path from "node:path";
import { expect, test, type Browser, type Page } from "@playwright/test";
import { director, scriptDir } from "../playwright.config";

// The test stack's settings (tests/e2e/harness/stack.go): each vote lasts two
// seconds and the premiere counts down from one, so a whole performance takes
// about twenty seconds. The stand-in language model offers "Turn N, first
// option." and so on, and the stand-in recordings are a quarter of a second
// of silence.
const TURNS = 8;

// A 1x1 PNG. The server checks that an upload really is an image.
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
  "base64",
);

// Logs the director in and creates a room, ending on the room page.
async function createRoom(page: Page, topic: string): Promise<string> {
  await page.goto("/login");
  await page.getByLabel("Username").fill(director.username);
  await page.getByLabel("Password").fill(director.password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/rooms$/);

  await page.goto("/rooms/new");
  await page.getByLabel("Topic").fill(topic);
  const a = page.getByRole("group", { name: "Character A" });
  await a.getByLabel("Name").fill("Grumble");
  await a.getByLabel("Portrait").setInputFiles({ name: "a.png", mimeType: "image/png", buffer: PNG });
  const b = page.getByRole("group", { name: "Character B" });
  await b.getByLabel("Name").fill("Fizz");
  await b.getByLabel("Portrait").setInputFiles({ name: "b.png", mimeType: "image/png", buffer: PNG });
  await page.getByRole("button", { name: "Create room" }).click();

  await expect(page).toHaveURL(/\/rooms\/[0-9a-f-]{36}$/);
  return page.url().split("/").pop()!;
}

// An audience member: their own browser, with no cookies but their own.
async function audienceMember(browser: Browser, shareLink: string): Promise<Page> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(shareLink);
  return page;
}

const script = (page: Page) => page.getByRole("list", { name: "Script" }).getByRole("listitem");
const choices = (page: Page) => page.getByRole("group", { name: "Choices" }).getByRole("button");
const premiere = (page: Page) => page.getByTestId("premiere");

// The eight recordings have each played to their end.
async function expectPlayedThrough(page: Page) {
  await expect(page.getByText("The performance has finished.")).toBeVisible({ timeout: 20_000 });
  await expect(premiere(page)).toHaveAttribute("data-played", String(TURNS));
  await expect(script(page)).toHaveCount(TURNS);
}

test.describe.serial("a performance, from the pages", () => {
  test.setTimeout(120_000);

  let directorPage: Page;
  let audience: Page[];
  let shareLink: string;

  test.afterAll(async () => {
    for (const page of [directorPage, ...(audience ?? [])]) await page?.context().close();
  });

  test("a director and two audience members vote through a performance and hear its premiere", async ({ browser }) => {
    directorPage = await (await browser.newContext()).newPage();
    const roomId = await createRoom(directorPage, `A biscuit goes missing ${Date.now()}`);
    shareLink = await directorPage.getByLabel("Share link").inputValue();

    // Turn 1's line takes two failed attempts to voice, a few seconds, so
    // the page stays on its winner long enough to be read here.
    fs.writeFileSync(
      path.join(scriptDir, `${roomId}.json`),
      JSON.stringify({ failures: [{ op: "speech", turn: 1, attempts: 2 }] }),
    );

    // Two people open the link. Nothing happens until they enter.
    audience = [await audienceMember(browser, shareLink), await audienceMember(browser, shareLink)];
    for (const page of audience) {
      await expect(page.getByRole("heading", { name: /A biscuit goes missing/ })).toHaveCount(0);
      await page.getByRole("button", { name: "Enter theatre" }).click();
      await expect(page.getByRole("heading", { name: /A biscuit goes missing/ })).toBeVisible();
      await expect(page.getByText("Waiting for the director to start the performance.")).toBeVisible();
      await expect(page.getByRole("img", { name: "Portrait of Grumble" })).toBeVisible();
    }
    await directorPage.reload();
    await expect(directorPage.getByText("2 of 10 joined")).toBeVisible();

    await directorPage.getByRole("button", { name: "Start performance" }).click();
    const everyone = [directorPage, ...audience];

    // Turn 1: everyone is offered the same three lines and a countdown.
    for (const page of everyone) {
      await expect(choices(page)).toHaveText(
        ["Turn 1, first option.", "Turn 1, second option.", "Turn 1, third option."],
        { timeout: 15_000 },
      );
      await expect(page.getByRole("timer")).toBeVisible();
    }
    await expect(directorPage.getByRole("button", { name: "Start performance" })).toHaveCount(0);

    // The audience outvotes the director, two to one.
    await directorPage.getByRole("button", { name: "Turn 1, first option." }).click();
    for (const page of audience) await page.getByRole("button", { name: "Turn 1, second option." }).click();
    for (const page of audience) {
      await expect(page.getByRole("button", { name: "Turn 1, second option." })).toHaveAttribute("aria-pressed", "true");
      await expect(page.getByRole("button", { name: "Turn 1, first option." })).toBeDisabled();
    }

    // The round closes: everyone sees which line won and how.
    for (const page of everyone) {
      const winner = page.getByRole("region", { name: "The chosen line" });
      await expect(winner).toContainText("Turn 1, second option.", { timeout: 10_000 });
      await expect(winner).toContainText("It had the most votes.");
      await expect(winner.getByRole("listitem").filter({ hasText: "Turn 1, second option." })).toContainText("2 votes");
      await expect(winner.getByRole("listitem").filter({ hasText: "Turn 1, first option." })).toContainText("1 vote");
      await expect(page.getByText("Preparing the next line…")).toBeVisible();
      await expect(choices(page)).toHaveCount(0);
    }

    // Turn 2 opens with the first line in the script. Nobody votes from
    // here on, so each later line is picked at random.
    for (const page of everyone) {
      await expect(choices(page).first()).toHaveText("Turn 2, first option.", { timeout: 15_000 });
      await expect(script(page)).toHaveText(["Grumble: Turn 1, second option."]);
    }

    // The premiere: a countdown, then the eight recordings in order.
    await expect(directorPage.getByText(/The premiere starts in/)).toBeVisible({ timeout: 40_000 });
    for (const page of everyone) {
      await expectPlayedThrough(page);
      await expect(script(page).first()).toHaveText("Grumble: Turn 1, second option.");
      await expect(script(page).last()).toContainText("Fizz: Turn 8,");
      await expect(page.getByRole("button", { name: "Play again" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Tap to hear audio" })).toHaveCount(0);
    }
  });

  test("a finished room is replayed, by a newcomer and after a reload", async ({ browser }) => {
    // Someone who opens the link only now sees the script and can play it.
    // There is no countdown: the premiere has been.
    const newcomer = await audienceMember(browser, shareLink);
    audience.push(newcomer);
    await newcomer.getByRole("button", { name: "Enter theatre" }).click();
    await expect(script(newcomer)).toHaveCount(TURNS);
    await expect(newcomer.getByText(/The premiere starts in/)).toHaveCount(0);
    await expect(premiere(newcomer)).toHaveAttribute("data-played", "0");
    await newcomer.getByRole("button", { name: "Play performance" }).click();
    await expectPlayedThrough(newcomer);

    // And again, from the button that replaces it.
    await newcomer.getByRole("button", { name: "Play again" }).click();
    await expect(premiere(newcomer)).toHaveAttribute("data-played", "0");
    await expectPlayedThrough(newcomer);

    // Someone who was there reloads: they enter again, as the same person.
    const returning = audience[0];
    await returning.reload();
    await returning.getByRole("button", { name: "Enter theatre" }).click();
    await returning.getByRole("button", { name: "Play performance" }).click();
    await expectPlayedThrough(returning);

    // The director's page shows the same thing without a click to enter,
    // and the reload above did not take another seat: two, plus the newcomer.
    await directorPage.reload();
    await expect(directorPage.getByText("3 of 10 joined")).toBeVisible();
    await directorPage.getByRole("button", { name: "Play performance" }).click();
    await expectPlayedThrough(directorPage);
  });

  test("a link that is not valid says so", async ({ browser }) => {
    const page = await audienceMember(browser, shareLink.replace(/[\w-]+$/, "not-the-token"));
    audience.push(page);
    await page.getByRole("button", { name: "Enter theatre" }).click();
    await expect(page.getByRole("alert").filter({ hasText: "This link is not valid." })).toBeVisible();
    await expect(page.getByRole("button", { name: "Enter theatre" })).toBeVisible();
  });
});
