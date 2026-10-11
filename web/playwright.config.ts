import os from "node:os";
import path from "node:path";
import { defineConfig, devices } from "@playwright/test";

// The browser tests bring their own backend: the same isolated stack the Go
// end-to-end tests use, started by tests/e2e/cmd/stack, and this web app
// pointed at it. Fixed ports are used so each side knows where the other is;
// they differ from the `make dev` ports, so both can run at once.
const API_PORT = 18080;
const WEB_PORT = 13000;
const API_ORIGIN = `http://127.0.0.1:${API_PORT}`;
const WEB_ORIGIN = `http://127.0.0.1:${WEB_PORT}`;

// One director per run. The run ID is put in the environment so the worker
// processes, which load this file again, get the same value.
const runId = (process.env.E2E_RUN_ID ??= String(Date.now()));
export const director = {
  username: `web-e2e-${runId}`,
  password: `web-e2e-password-${runId}`,
};

// Where the stand-in providers read per-room scripts. A test writes
// <roomId>.json here before pressing Start, to slow a turn down or make one
// fail (tests/e2e/fixtures/script.go has the format).
export const scriptDir = path.join(os.tmpdir(), `monstertheatre-web-e2e-scripts-${runId}`);

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  timeout: 30_000,
  use: {
    baseURL: WEB_ORIGIN,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: [
    {
      // Built first and run with exec, so the stop signal reaches the stack
      // itself and it can shut its child processes down.
      command:
        `go build -o .bin/stack ./cmd/stack && exec ./.bin/stack -port ${API_PORT} ` +
        `-public-base-url ${WEB_ORIGIN} -director-username ${director.username} -script-dir ${scriptDir}`,
      cwd: "../tests/e2e",
      env: { STACK_DIRECTOR_PASSWORD: director.password },
      url: `${API_ORIGIN}/healthz`,
      timeout: 180_000,
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 20_000 },
      stdout: "pipe",
      stderr: "pipe",
    },
    {
      command: `npm run dev -- --port ${WEB_PORT} --hostname 127.0.0.1`,
      env: { API_ORIGIN },
      url: `${WEB_ORIGIN}/login`,
      timeout: 120_000,
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 10_000 },
    },
  ],
});
