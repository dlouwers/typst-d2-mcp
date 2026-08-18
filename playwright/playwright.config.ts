import { defineConfig, devices } from "@playwright/test";

// Browser-level checks for the admin UI: the things a Go test cannot
// reach. Chief among them is SameSite cookie behaviour, which is
// enforced by the browser and by nothing else — it is the only CSRF
// defence the admin actions have.
//
// Two hostnames are used so "cross-site" is genuinely cross-site.
// SameSite compares registrable domains and ignores the port, so
// 127.0.0.1:18081 -> 127.0.0.1:18080 would be *same* site and would
// prove nothing. `admin.test` and `evil.test` are distinct sites;
// --host-resolver-rules maps both to the loopback interface, so no DNS
// and no /etc/hosts entry is involved.

const PORT = Number(process.env.E2E_PORT ?? 18080);
const EVIL_PORT = Number(process.env.E2E_EVIL_PORT ?? 18081);

export const APP_ORIGIN = `http://admin.test:${PORT}`;
export const EVIL_ORIGIN = `http://evil.test:${EVIL_PORT}`;

export default defineConfig({
  testDir: "./tests",
  // The server keeps a single SQLite file; parallel workers would race
  // on the invite/user rows the tests create and delete.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI
    ? [["github"], ["html", { open: "never" }], ["list"]]
    : [["html", { open: "never" }], ["list"]],
  use: {
    baseURL: APP_ORIGIN,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "off",
    launchOptions: {
      args: [
        `--host-resolver-rules=MAP admin.test 127.0.0.1, MAP evil.test 127.0.0.1`,
      ],
      // Escape hatch for machines that already have a Chromium and
      // would rather not pull Playwright's pinned build (a ~150MB
      // download). CI leaves this unset and uses the pinned one.
      ...(process.env.E2E_CHROMIUM_PATH
        ? { executablePath: process.env.E2E_CHROMIUM_PATH }
        : {}),
    },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: "../scripts/e2e-server.sh",
    // Health check on the loopback address: the resolver rules above
    // apply inside the browser, not to Playwright's own probe.
    url: `http://127.0.0.1:${PORT}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
