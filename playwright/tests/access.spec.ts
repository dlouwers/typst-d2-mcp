import { test, expect } from "@playwright/test";
import { APP_ORIGIN } from "../playwright.config";
import { signIn, sessionValue } from "../helpers/session";

// Access control as a browser sees it, including the cookie attributes
// the CSRF defence rests on.

test.describe("access control", () => {
  test("an unauthenticated visit lands on the sign-in page", async ({ page }) => {
    await page.goto(`${APP_ORIGIN}/admin/`);
    await expect(page).toHaveURL(/\/admin\/signin$/);
    await expect(page.getByRole("link", { name: /Sign in with GitHub/ })).toBeVisible();
  });

  test("the sign-in link goes to GitHub with an admin-scoped state", async ({ page }) => {
    // Stop at the redirect rather than actually leaving for github.com.
    await page.route("https://github.com/**", (route) =>
      route.fulfill({ contentType: "text/html", body: "<html>github</html>" }),
    );
    const [request] = await Promise.all([
      page.waitForRequest("https://github.com/login/oauth/authorize**"),
      page.goto(`${APP_ORIGIN}/admin/login`),
    ]);
    const state = new URL(request.url()).searchParams.get("state") ?? "";
    // The prefix is what lets the shared callback tell an admin login
    // apart from an MCP client's authorization.
    expect(state.startsWith("ttd2adm_")).toBe(true);
  });

  test("a non-admin session is refused", async ({ context, page }) => {
    await signIn(context, APP_ORIGIN, "somebodyelse");
    const response = await page.goto(`${APP_ORIGIN}/admin/`);
    expect(response?.status()).toBe(403);
  });

  test("a tampered session is treated as absent", async ({ context, page }) => {
    await context.addCookies([
      {
        name: "ttd2_admin_session",
        value: sessionValue("e2eadmin").slice(0, -3) + "xxx",
        domain: "admin.test",
        path: "/",
      },
    ]);
    await page.goto(`${APP_ORIGIN}/admin/`);
    await expect(page).toHaveURL(/\/admin\/signin$/);
  });

  test("the session cookie is HttpOnly and SameSite=Lax", async ({ context }) => {
    await signIn(context, APP_ORIGIN);
    const [cookie] = (await context.cookies(APP_ORIGIN)).filter(
      (c) => c.name === "ttd2_admin_session",
    );
    expect(cookie.httpOnly).toBe(true);
    // Lax is the CSRF defence; Strict would break the return leg of the
    // GitHub round trip that sets this cookie in the first place.
    expect(cookie.sameSite).toBe("Lax");
  });

  test("pages load only vendored assets, no external hosts", async ({ context, page }) => {
    await signIn(context, APP_ORIGIN);
    const external: string[] = [];
    page.on("request", (r) => {
      const host = new URL(r.url()).hostname;
      if (host !== "admin.test") external.push(r.url());
    });
    await page.goto(`${APP_ORIGIN}/admin/`);
    await page.waitForLoadState("networkidle");
    expect(external).toEqual([]);
  });
});
