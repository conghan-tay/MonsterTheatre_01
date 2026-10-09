import { expect, test, type Page } from "@playwright/test";
import { director } from "../playwright.config";

// A 1x1 PNG. The server checks that an upload really is an image.
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
  "base64",
);

// Logs in and creates a room through the pages, ending on the room page.
async function createRoom(page: Page): Promise<string> {
  await page.goto("/login");
  await page.getByLabel("Username").fill(director.username);
  await page.getByLabel("Password").fill(director.password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/rooms$/);

  await page.goto("/rooms/new");
  await page.getByLabel("Topic").fill(`A WebSocket test ${Date.now()}`);
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

// Opens a WebSocket from the page and resolves with its first message, or
// with how it failed.
function firstMessage(page: Page, url: string): Promise<string> {
  return page.evaluate(
    (url) =>
      new Promise<string>((resolve) => {
        const ws = new WebSocket(url);
        ws.onmessage = (event) => {
          resolve(String(event.data));
          ws.close();
        };
        ws.onerror = () => resolve("error");
        ws.onclose = (event) => resolve(`closed ${event.code}`);
      }),
    url,
  );
}

const waiting = { type: "snapshot", revision: 0, state: { phase: "waiting", turn: 0 } };

// No page opens the WebSocket yet (the theatre pages are step 14). These
// tests open it from a real browser on the web app's origin, which proves
// what the Go tests cannot: the cookie is sent, the Origin the browser
// chooses is accepted, and the /api proxy carries the upgrade.
test("the room's WebSocket works through the web app's own origin", async ({ page }) => {
  const roomId = await createRoom(page);
  const url = `ws://${new URL(page.url()).host}/api/v1/rooms/${roomId}/ws`;
  expect(JSON.parse(await firstMessage(page, url))).toEqual(waiting);
});

// The other route a page could take locally: straight to the Go server's
// port. Cookies are not tied to a port, so the session still arrives.
test("the room's WebSocket works straight to the API server's port", async ({ page }) => {
  const roomId = await createRoom(page);
  const url = `ws://127.0.0.1:18080/api/v1/rooms/${roomId}/ws`;
  expect(JSON.parse(await firstMessage(page, url))).toEqual(waiting);
});

test("a page on another origin cannot open the room's WebSocket", async ({ page, context }) => {
  const roomId = await createRoom(page);
  const url = `ws://127.0.0.1:18080/api/v1/rooms/${roomId}/ws`;

  // Same browser and cookie jar, but a page served from somewhere else.
  // Chromium may also withhold the SameSite=Lax cookie here; this test does
  // not tell the two refusals apart. The Go tests (S5) prove the Origin
  // check on its own, with a valid cookie attached.
  const elsewhere = await context.newPage();
  await elsewhere.route("http://elsewhere.test/", (route) =>
    route.fulfill({ contentType: "text/html", body: "<title>elsewhere</title>" }),
  );
  await elsewhere.goto("http://elsewhere.test/");
  expect(await firstMessage(elsewhere, url)).toMatch(/^(error|closed 1006)$/);
});
