import { expect, test, type Page } from "@playwright/test";
import { director } from "../playwright.config";

// A 1x1 PNG. The server checks that an upload really is an image.
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
  "base64",
);

async function logIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("Username").fill(director.username);
  await page.getByLabel("Password").fill(director.password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/rooms$/);
}

test("a director logs in, creates a room, and sees its share link", async ({ page }) => {
  await logIn(page);

  await page.getByRole("link", { name: "New room" }).click();
  await expect(page).toHaveURL(/\/rooms\/new$/);

  const topic = `Two monsters argue about a biscuit ${Date.now()}`;
  await page.getByLabel("Topic").fill(topic);
  await page.getByLabel("Description").fill("A cramped kitchen at midnight.");
  await page.getByLabel("Genre").fill("comedy");

  const a = page.getByRole("group", { name: "Character A" });
  await a.getByLabel("Name").fill("Grumble");
  await a.getByLabel("Brief").fill("A tired swamp monster who denies everything.");
  await a.getByLabel("Portrait").setInputFiles({ name: "grumble.png", mimeType: "image/png", buffer: PNG });

  const b = page.getByRole("group", { name: "Character B" });
  await b.getByLabel("Name").fill("Fizz");
  await b.getByLabel("Brief").fill("An excitable imp with crumbs on its face.");
  await b.getByLabel("Portrait").setInputFiles({ name: "fizz.png", mimeType: "image/png", buffer: PNG });

  await page.getByRole("button", { name: "Create room" }).click();

  // The room page.
  await expect(page).toHaveURL(/\/rooms\/[0-9a-f-]{36}$/);
  const roomId = page.url().split("/").pop()!;
  await expect(page.getByRole("heading", { name: topic })).toBeVisible();
  await expect(page.getByText("Grumble")).toBeVisible();
  await expect(page.getByText("Fizz")).toBeVisible();

  const shareLink = page.getByLabel("Share link");
  await expect(shareLink).toHaveValue(new RegExp(`/join/${roomId}/[\\w-]{43}$`));
  await expect(page.getByText("0 of 10 joined")).toBeVisible();

  // Both portraits actually load, through the proxy, with the session cookie.
  for (const name of ["Portrait of Grumble", "Portrait of Fizz"]) {
    const portrait = page.getByRole("img", { name });
    await expect(portrait).toBeVisible();
    await expect
      .poll(() => portrait.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0))
      .toBe(true);
  }

  // Start is shown but not wired up yet.
  await expect(page.getByRole("button", { name: "Start performance" })).toBeDisabled();

  // The link is the same after a reload.
  const linkBefore = await shareLink.inputValue();
  await page.reload();
  await expect(page.getByLabel("Share link")).toHaveValue(linkBefore);

  // And the room is in the director's list.
  await page.getByRole("link", { name: "Your rooms" }).click();
  await expect(page).toHaveURL(/\/rooms$/);
  await expect(page.getByRole("link", { name: topic })).toBeVisible();
});

test("a wrong password shows an error and stays on the login page", async ({ page }) => {
  await page.goto("/login");
  await page.getByLabel("Username").fill(director.username);
  await page.getByLabel("Password").fill("not-the-password");
  await page.getByRole("button", { name: "Log in" }).click();

  // Next.js adds its own empty alert region for route announcements, so pick ours by its text.
  await expect(page.getByRole("alert").filter({ hasText: "Wrong username or password" })).toBeVisible();
  await expect(page).toHaveURL(/\/login$/);
});

test("opening the rooms page without logging in goes to the login page", async ({ page }) => {
  await page.goto("/rooms");
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole("button", { name: "Log in" })).toBeVisible();
});

// The form can upload about 10 MiB. This checks the /api proxy in front of the
// Go server passes a request that large through whole.
test("two portraits just under the size limit are accepted", async ({ page }) => {
  // A valid PNG followed by padding: still a PNG to the server, which reads
  // only the header to check the type and dimensions.
  const big = Buffer.concat([PNG, Buffer.alloc(5 * 1024 * 1024 - PNG.length - 1024)]);

  await logIn(page);
  await page.goto("/rooms/new");
  await page.getByLabel("Topic").fill("A very large portrait test");
  const a = page.getByRole("group", { name: "Character A" });
  await a.getByLabel("Name").fill("Big");
  await a.getByLabel("Portrait").setInputFiles({ name: "a.png", mimeType: "image/png", buffer: big });
  const b = page.getByRole("group", { name: "Character B" });
  await b.getByLabel("Name").fill("Bigger");
  await b.getByLabel("Portrait").setInputFiles({ name: "b.png", mimeType: "image/png", buffer: big });
  await page.getByRole("button", { name: "Create room" }).click();

  await expect(page).toHaveURL(/\/rooms\/[0-9a-f-]{36}$/, { timeout: 20_000 });
  await expect(page.getByRole("heading", { name: "A very large portrait test" })).toBeVisible();
});
