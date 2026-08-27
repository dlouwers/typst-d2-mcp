import { test, expect } from "@playwright/test";
import { APP_ORIGIN } from "../playwright.config";
import { signIn } from "../helpers/session";
import {
  countInvites,
  countUsers,
  quotaFor,
  seedSignedInUser,
  orgMemberRole,
} from "../helpers/db";

// The admin UI is supposed to work with JavaScript unavailable: the
// forms are ordinary POSTs, the handlers redirect with a banner instead
// of swapping a fragment, and the row panels are <details> rather than a
// scripted dialog.
//
// This is also the check on a design decision — the Stormlantern
// sl-input / sl-button components submit through ElementInternals, so
// they would take every action here down with them. These tests fail if
// anyone swaps the native controls back for them.

test.use({ javaScriptEnabled: false });

test.describe("without javascript", () => {
  test.beforeEach(async ({ context }) => {
    await signIn(context, APP_ORIGIN);
  });

  test("the page still renders its content", async ({ page }) => {
    await page.goto(`${APP_ORIGIN}/admin/`);
    // sl-* elements never upgrade, but they are display-only wrappers,
    // so their contents must still be readable.
    await expect(page.locator("#users")).toContainText("Login");
    await expect(page.getByRole("button", { name: "Invite" })).toBeVisible();
  });

  test("invite works as a plain form post and redirects with a banner", async ({ page }) => {
    await page.goto(`${APP_ORIGIN}/admin/`);
    await page.fill('#invite-login', "nojsinvitee");
    await page.click('form.invite button[type="submit"]');

    // The redirect lands back on the list with the message in the query.
    await expect(page).toHaveURL(/\/admin\/\?flash=/);
    await expect(page.locator("#flash")).toContainText("Invited nojsinvitee");
    await expect(page.locator("#users")).toContainText("nojsinvitee");
    expect(countInvites("nojsinvitee")).toBe(1);
  });

  test("the row panel opens without script and quota can be set", async ({ page }) => {
    seedSignedInUser(5252, "nojsquota");
    await page.goto(`${APP_ORIGIN}/admin/`);

    const row = page.locator("tr", { hasText: "nojsquota" });
    // <details> is native HTML; clicking the summary works with no JS.
    await row.locator("summary").click();
    await row.locator('form[action="/admin/quota"] input[name="mode"][value="unlimited"]').check();
    await row.getByRole("button", { name: "Save quota" }).click();

    await expect(page.locator("#flash")).toContainText("Quota updated");
    expect(quotaFor("nojsquota")).toBe("0");
  });

  // Ownership is the only route to a publishable organisation namespace,
  // so the buttons that grant it must not be the one part of the admin
  // UI that needs script.
  test("ownership can be granted and the last owner protected without script", async ({
    page,
  }) => {
    seedSignedInUser(5260, "nojsowner");
    await page.goto(`${APP_ORIGIN}/admin/orgs`);
    await page.fill('form[action="/admin/orgs/create"] input[name="slug"]', "nojsorg");
    await page.click('form[action="/admin/orgs/create"] button[type="submit"]');

    const card = () => page.locator("sl-card.org", { hasText: "@nojsorg" });
    await card().locator('form[action="/admin/orgs/members/add"] input[name="login"]').fill("nojsowner");
    await card().getByRole("button", { name: "Add" }).click();
    expect(orgMemberRole("nojsorg", "nojsowner")).toBe("member");

    await card().getByRole("button", { name: "Make owner" }).click();
    await expect(page).toHaveURL(/\/admin\/orgs\?flash=/);
    await expect(page.locator("#flash")).toContainText("is now an owner");
    expect(orgMemberRole("nojsorg", "nojsowner")).toBe("owner");

    await card().getByRole("button", { name: "Make member" }).click();
    await expect(page.locator("#flash")).toContainText("only owner");
    expect(orgMemberRole("nojsorg", "nojsowner")).toBe("owner");

    await card().getByRole("button", { name: "Delete" }).click();
    await expect(page.locator("#orgs")).not.toContainText("@nojsorg");
  });

  test("delete still enforces the typed confirmation", async ({ page }) => {
    seedSignedInUser(5253, "nojsdelete");
    await page.goto(`${APP_ORIGIN}/admin/`);

    let row = page.locator("tr", { hasText: "nojsdelete" });
    await row.locator("summary").click();
    await row.locator('input[name="confirm"]').fill("wrong");
    await row.getByRole("button", { name: "Delete user and all data" }).click();
    await expect(page.locator("#flash")).toContainText("type the login exactly");
    expect(countUsers("nojsdelete")).toBe(1);

    row = page.locator("tr", { hasText: "nojsdelete" });
    await row.locator("summary").click();
    await row.locator('input[name="confirm"]').fill("nojsdelete");
    await row.getByRole("button", { name: "Delete user and all data" }).click();
    await expect(page.locator("#flash")).toContainText("Deleted nojsdelete");
    expect(countUsers("nojsdelete")).toBe(0);
  });
});
