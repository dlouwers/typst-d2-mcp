import { test, expect } from "@playwright/test";
import { APP_ORIGIN } from "../playwright.config";
import { signIn } from "../helpers/session";
import { seedSignedInUser, orgCount, orgMemberCount } from "../helpers/db";

// Organisation management is a second admin page (#63 rung 3). These
// checks drive the create / add-member / remove / delete flow in a real
// browser, proving the htmx swaps and native-form fallbacks work.

test.describe("organisations", () => {
  test.beforeEach(async ({ context, page }) => {
    await signIn(context, APP_ORIGIN);
    await page.goto(`${APP_ORIGIN}/admin/orgs`);
  });

  test("create an organisation, add and remove a member, then delete", async ({
    page,
  }) => {
    // Create.
    await page.locator('form[action="/admin/orgs/create"] input[name="slug"]').fill("acme");
    await page
      .locator('form[action="/admin/orgs/create"] input[name="display_name"]')
      .fill("Acme Corp");
    await page.getByRole("button", { name: "Create organisation" }).click();

    await expect(page.locator("#orgs")).toContainText("@acme");
    await expect(page.locator("#orgs")).toContainText("Acme Corp");
    expect(orgCount()).toBe(1);

    // Add a signed-in user as a member.
    seedSignedInUser(4300, "orgmember");
    const card = page.locator("sl-card.org", { hasText: "@acme" });
    await card.locator('form[action="/admin/orgs/members/add"] input[name="login"]').fill("orgmember");
    await card.getByRole("button", { name: "Add" }).click();

    await expect(page.locator("#orgs")).toContainText("orgmember");
    expect(orgMemberCount("acme")).toBe(1);

    // Remove the member.
    await page
      .locator("sl-card.org", { hasText: "@acme" })
      .getByRole("button", { name: "Remove" })
      .click();
    await expect(page.locator("#orgs")).not.toContainText("orgmember");
    expect(orgMemberCount("acme")).toBe(0);

    // Delete the organisation.
    await page
      .locator("sl-card.org", { hasText: "@acme" })
      .getByRole("button", { name: "Delete" })
      .click();
    await expect(page.locator("#orgs")).not.toContainText("@acme");
    expect(orgCount()).toBe(0);
  });

  test("a malformed slug is rejected with a flash", async ({ page }) => {
    await page.locator('form[action="/admin/orgs/create"] input[name="slug"]').fill("Acme Corp");
    await page.getByRole("button", { name: "Create organisation" }).click();
    await expect(page.locator("#flash")).toContainText("lowercase");
    expect(orgCount()).toBe(0);
  });
});
