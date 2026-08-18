import { test, expect, type Page } from "@playwright/test";
import { APP_ORIGIN } from "../playwright.config";
import { signIn } from "../helpers/session";
import {
  auditActions,
  countInvites,
  countUsers,
  quotaFor,
  seedAPIKey,
  seedQuota,
  seedSignedInUser,
} from "../helpers/db";

// Every admin action replies in dual mode: htmx gets an out-of-band
// flash plus a refreshed table, a plain form post gets a redirect. The
// Go tests cover only the invite action under htmx; these cover the rest,
// and prove the swap really happens in the browser without a reload.

/** Marks the current document; a full page load clears it. */
async function markDocument(page: Page): Promise<void> {
  await page.evaluate(() => {
    (window as unknown as { __alive?: boolean }).__alive = true;
  });
}

async function documentSurvived(page: Page): Promise<boolean> {
  return page.evaluate(
    () => (window as unknown as { __alive?: boolean }).__alive === true,
  );
}

/** Opens the per-row Manage panel for a login. */
async function openRow(page: Page, login: string) {
  const row = page.locator("tr", { hasText: login });
  await row.locator("summary").click();
  return row;
}

test.describe("htmx actions", () => {
  test.beforeEach(async ({ context, page }) => {
    await signIn(context, APP_ORIGIN);
    await page.goto(`${APP_ORIGIN}/admin/`);
    // htmx must actually be running, or these tests would silently
    // degrade into the plain-form-post path and prove nothing.
    await expect
      .poll(() =>
        page.evaluate(
          () => typeof (window as unknown as { htmx?: unknown }).htmx,
        ),
      )
      .toBe("object");
  });

  test("invite swaps the table in place, without reloading", async ({ page }) => {
    await markDocument(page);

    await page.fill('#invite-login', "htmxinvitee");
    await page.click('form.invite button[type="submit"]');

    await expect(page.locator("#users")).toContainText("htmxinvitee");
    await expect(page.locator("#flash")).toContainText("Invited htmxinvitee");
    expect(await documentSurvived(page)).toBe(true);
    expect(countInvites("htmxinvitee")).toBe(1);
  });

  test("setting a fixed quota updates the row", async ({ page }) => {
    seedSignedInUser(4242, "quotauser");
    await page.reload();

    const row = await openRow(page, "quotauser");
    await row.locator('input[name="mode"][value="fixed"]').check();
    await row.locator('input[name="value"]').fill("12");
    await markDocument(page);
    await row.getByRole("button", { name: "Save quota" }).click();

    await expect(page.locator("#flash")).toContainText("Quota updated");
    await expect(page.locator("tr", { hasText: "quotauser" })).toContainText("12/day");
    expect(await documentSurvived(page)).toBe(true);
    expect(quotaFor("quotauser")).toBe("12");
  });

  test("unlimited quota renders as unlimited", async ({ page }) => {
    seedSignedInUser(4243, "unlimiteduser");
    await page.reload();

    const row = await openRow(page, "unlimiteduser");
    await row.locator('input[name="mode"][value="unlimited"]').check();
    await row.getByRole("button", { name: "Save quota" }).click();

    await expect(page.locator("tr", { hasText: "unlimiteduser" })).toContainText(
      "unlimited",
    );
    expect(quotaFor("unlimiteduser")).toBe("0");
  });

  test("reset today's counter reports back", async ({ page }) => {
    seedSignedInUser(4244, "resetuser");
    await page.reload();

    const row = await openRow(page, "resetuser");
    await markDocument(page);
    await row.getByRole("button", { name: "Reset today's counter" }).click();

    await expect(page.locator("#flash")).toContainText("counter reset");
    expect(await documentSurvived(page)).toBe(true);
  });

  test("revoking API keys reports the count and clears the badge", async ({ page }) => {
    seedSignedInUser(4245, "keyuser");
    seedAPIKey("keyuser");
    await page.reload();
    await expect(page.locator("tr", { hasText: "keyuser" })).not.toContainText("none");

    const row = await openRow(page, "keyuser");
    await row.getByRole("button", { name: "Revoke API keys" }).click();

    await expect(page.locator("#flash")).toContainText("Revoked 1 key(s)");
    await expect(page.locator("tr", { hasText: "keyuser" })).toContainText("none");
  });

  test("revoking access removes the invite", async ({ page }) => {
    await page.fill('#invite-login', "revokeme");
    await page.click('form.invite button[type="submit"]');
    await expect(page.locator("#users")).toContainText("revokeme");

    const row = await openRow(page, "revokeme");
    await row.getByRole("button", { name: "Revoke access" }).click();

    await expect(page.locator("#flash")).toContainText("effective immediately");
    expect(countInvites("revokeme")).toBe(0);
  });

  test("delete requires the typed login and then removes the user", async ({ page }) => {
    seedSignedInUser(4246, "deleteme");
    await page.reload();

    // Wrong confirmation is refused.
    let row = await openRow(page, "deleteme");
    await row.locator('input[name="confirm"]').fill("deletem");
    await row.getByRole("button", { name: "Delete user and all data" }).click();
    await expect(page.locator("#flash")).toContainText("type the login exactly");
    expect(countUsers("deleteme")).toBe(1);

    // Correct confirmation goes through.
    row = await openRow(page, "deleteme");
    await row.locator('input[name="confirm"]').fill("deleteme");
    await row.getByRole("button", { name: "Delete user and all data" }).click();
    await expect(page.locator("#flash")).toContainText("Deleted deleteme");
    await expect(page.locator("#users")).not.toContainText("deleteme");
    expect(countUsers("deleteme")).toBe(0);
  });

  test("every action reached above is recorded in the audit log", async ({ page }) => {
    const actions = new Set(auditActions());
    for (const action of [
      "invite",
      "set_quota",
      "reset_today",
      "revoke_keys",
      "revoke_access",
      "delete_user",
    ]) {
      expect(actions.has(action), `audit log is missing ${action}`).toBe(true);
    }

    await page.goto(`${APP_ORIGIN}/admin/audit`);
    await expect(page.locator("table")).toContainText("delete_user");
  });
});

// Regression: a user already set to Unlimited stores quota_per_day = 0,
// which the template prefilled into the "fixed" number input. With
// min="1" on that input the browser refused to submit the form at all,
// so an unlimited user could not be moved back to Default or re-saved
// as Unlimited — the button simply did nothing.
//
// Browser-level by necessity: HTML constraint validation is enforced by
// the browser, so no Go test could see it. It also survives JavaScript
// being off, which is why the constraint could not just be toggled.
test.describe("quota form validation", () => {
  test.beforeEach(async ({ context, page }) => {
    await signIn(context, APP_ORIGIN);
    await page.goto(`${APP_ORIGIN}/admin/`);
  });

  test("an unlimited user can be set back to Default", async ({ page }) => {
    seedSignedInUser(7001, "wasunlimited");
    seedQuota("wasunlimited", 0);
    await page.reload();
    await expect(page.locator("tr", { hasText: "wasunlimited" })).toContainText("unlimited");

    const row = await openRow(page, "wasunlimited");
    await row.locator('input[name="mode"][value="default"]').check();
    await row.getByRole("button", { name: "Save quota" }).click();

    await expect(page.locator("#flash")).toContainText("Quota updated");
    expect(quotaFor("wasunlimited")).toBe("NULL");
  });

  test("an unlimited user can be re-saved as Unlimited", async ({ page }) => {
    seedSignedInUser(7002, "stayunlimited");
    seedQuota("stayunlimited", 0);
    await page.reload();

    const row = await openRow(page, "stayunlimited");
    await row.locator('input[name="mode"][value="unlimited"]').check();
    await row.getByRole("button", { name: "Save quota" }).click();

    await expect(page.locator("#flash")).toContainText("Quota updated");
    expect(quotaFor("stayunlimited")).toBe("0");
  });

  // A junk value in the box must not block an unrelated mode either; the
  // server decides, and only when Fixed is actually selected.
  test("a junk value in the fixed box does not block saving Default", async ({ page }) => {
    seedSignedInUser(7003, "junkvalue");
    seedQuota("junkvalue", 5);
    await page.reload();

    const row = await openRow(page, "junkvalue");
    await row.locator('input[name="value"]').fill("0");
    await row.locator('input[name="mode"][value="default"]').check();
    await row.getByRole("button", { name: "Save quota" }).click();

    await expect(page.locator("#flash")).toContainText("Quota updated");
    expect(quotaFor("junkvalue")).toBe("NULL");
  });
});
