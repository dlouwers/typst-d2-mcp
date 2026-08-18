import { test, expect } from "@playwright/test";
import { APP_ORIGIN, EVIL_ORIGIN } from "../playwright.config";
import { signIn } from "../helpers/session";
import { countInvites } from "../helpers/db";

// The admin actions have exactly one CSRF defence: the session cookie is
// SameSite=Lax, so browsers do not attach it to a cross-site POST. That
// is enforced by the browser and by nothing in the Go code, which is why
// it cannot be tested anywhere but here.
//
// admin.test and evil.test are distinct registrable domains, so this is
// genuinely cross-*site*. Two ports on 127.0.0.1 would not be: SameSite
// ignores the port.

const attackPage = (target: string, login: string) => `<!doctype html>
<html><body>
  <form id="attack" method="post" action="${target}/admin/invite">
    <input type="hidden" name="login" value="${login}">
  </form>
  <script>document.getElementById('attack').submit();</script>
</body></html>`;

test.describe("cross-site request forgery", () => {
  test("a cross-site POST cannot invite, even with a valid session", async ({
    context,
    page,
  }) => {
    await signIn(context, APP_ORIGIN);

    // Positive control first: the session really does work same-site.
    // Without this, a broken app would make the test below pass for the
    // wrong reason.
    await page.goto(`${APP_ORIGIN}/admin/`);
    await expect(page.locator("#users")).toBeVisible();

    await page.route(`${EVIL_ORIGIN}/attack`, (route) =>
      route.fulfill({
        contentType: "text/html",
        body: attackPage(APP_ORIGIN, "csrfvictim"),
      }),
    );

    const response = await Promise.all([
      page.waitForResponse(
        (r) => r.url() === `${APP_ORIGIN}/admin/invite` && r.request().method() === "POST",
      ),
      page.goto(`${EVIL_ORIGIN}/attack`),
    ]).then(([r]) => r);

    // No cookie travels with the cross-site POST, so the server sees an
    // unauthenticated request and refuses it outright.
    expect(response.status()).toBe(401);
    expect(countInvites("csrfvictim")).toBe(0);
  });

  test("the same request from the app's own origin succeeds", async ({
    context,
    page,
  }) => {
    await signIn(context, APP_ORIGIN);
    await page.goto(`${APP_ORIGIN}/admin/`);

    await page.fill('#invite-login', "samesiteinvitee");
    await page.click('form.invite button[type="submit"]');

    await expect(page.locator("#users")).toContainText("samesiteinvitee");
    expect(countInvites("samesiteinvitee")).toBe(1);
  });
});
