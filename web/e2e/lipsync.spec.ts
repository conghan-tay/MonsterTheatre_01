import fs from "node:fs";
import path from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { director, scriptDir } from "../playwright.config";

// A performance with lip sync, from the pages (D055, D056). The stand-in
// makes every video a one-second clip; a script slows or fails one turn's.

// A 1x1 PNG. The server checks that an upload really is an image.
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
  "base64",
);

async function createRoom(page: Page, topic: string): Promise<string> {
  await page.goto("/login");
  await page.getByLabel("Username").fill(director.username);
  await page.getByLabel("Password").fill(director.password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/rooms$/);

  await page.goto("/rooms/new");
  await page.getByLabel("Topic").fill(topic);
  for (const [group, name] of [["Character A", "Grumble"], ["Character B", "Fizz"]]) {
    const character = page.getByRole("group", { name: group });
    await character.getByLabel("Name").fill(name);
    await character.getByLabel("Portrait").setInputFiles({ name: "p.png", mimeType: "image/png", buffer: PNG });
  }
  await page.getByRole("button", { name: "Create room" }).click();
  await expect(page).toHaveURL(/\/rooms\/[0-9a-f-]{36}$/);
  return page.url().split("/").pop()!;
}

test("with lip sync, the room is told how many clips are ready, then each speaker's video plays", async ({ browser }) => {
  test.setTimeout(120_000);
  const directorPage = await (await browser.newContext()).newPage();
  const roomId = await createRoom(directorPage, "A lip-sync performance");
  const shareLink = await directorPage.getByLabel("Share link").inputValue();

  // Turn 1's video takes 25 s, so it is still being made when the turns
  // end, about 16 s in; turn 2's cannot be made at all.
  fs.writeFileSync(
    path.join(scriptDir, `${roomId}.json`),
    JSON.stringify({ lipSyncDelays: { "1": 25000 }, failures: [{ op: "lipsync", turn: 2, attempts: 1 }] }),
  );

  // The audience member is in before Start, with turns 2 and 3's
  // recordings held back: at the premiere the player waits for each of
  // them, holding turn 1, then turn 2, on stage.
  const member = await (await browser.newContext()).newPage();
  const hold = async (turn: number) => {
    let release!: () => void;
    const released = new Promise<void>((resolve) => (release = resolve));
    await member.route(`**/turns/${turn}/audio`, async (route) => {
      await released;
      await route.continue();
    });
    return release;
  };
  const releaseTwo = await hold(2);
  const releaseThree = await hold(3);
  await member.goto(shareLink);
  await member.getByLabel("Nickname").fill("Ana");
  await member.getByRole("button", { name: "Enter theatre" }).click();

  // Lip sync is asked for, not assumed.
  const lipSync = directorPage.getByLabel("Include lip-sync video");
  await expect(lipSync).not.toBeChecked();
  await lipSync.check();
  await directorPage.getByRole("button", { name: "Start performance" }).click();

  // After the turns: the last line's result, and how many clips are ready.
  await expect(member.getByText(/^[0-7] of 8 clips (has|have) been generated…$/)).toBeVisible({ timeout: 40_000 });
  await expect(member.getByRole("region", { name: "The chosen line" })).toContainText("Turn 8,");

  // The premiere: turn 1 is Grumble's video, playing without sound.
  const stage = member.getByRole("region", { name: "Stage" });
  await expect(stage).toBeVisible({ timeout: 40_000 });
  const video = stage.locator("video");
  await expect(video).toHaveCount(1);
  await expect(video).toHaveAccessibleName("Grumble speaking");
  await expect(stage.getByRole("img")).toHaveCount(0);
  expect(await video.evaluate((v: HTMLVideoElement) => v.muted)).toBe(true);
  await expect.poll(() => video.evaluate((v: HTMLVideoElement) => v.currentTime > 0)).toBe(true);
  await expect(stage).toHaveText(/^Turn 1, (first|second|third) option\.$/);

  // Turn 2's video could not be made: Fizz's portrait, as without lip sync.
  releaseTwo();
  await expect(stage.getByRole("img")).toHaveAccessibleName("Portrait of Fizz");
  await expect(stage.locator("video")).toHaveCount(0);

  releaseThree();
  await expect(member.getByText("The performance has finished.")).toBeVisible({ timeout: 20_000 });
  await expect(stage).toHaveCount(0);

  await member.context().close();
  await directorPage.context().close();
});
