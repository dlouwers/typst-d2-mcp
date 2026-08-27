import { test, expect } from "@playwright/test";
import { APP_ORIGIN } from "../playwright.config";
import { signIn } from "../helpers/session";
import {
  seedSignedInUser,
  orgCount,
  orgMemberCount,
  orgMemberRole,
} from "../helpers/db";

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

  // Ownership is the only thing that grants publishing, and it is only
  // reachable through this page — so if these controls do not work, an
  // organisation namespace can be created and then never published to.
  test("promote to owner, and refuse to strand the last one", async ({ page }) => {
    await page.locator('form[action="/admin/orgs/create"] input[name="slug"]').fill("acme");
    await page.getByRole("button", { name: "Create organisation" }).click();
    await expect(page.locator("#orgs")).toContainText("@acme");

    // Added straight as an owner, so the organisation is publishable
    // from the moment it has anyone in it.
    seedSignedInUser(4310, "orgowner");
    const card = () => page.locator("sl-card.org", { hasText: "@acme" });
    await card().locator('form[action="/admin/orgs/members/add"] input[name="login"]').fill("orgowner");
    await card().locator('form[action="/admin/orgs/members/add"] select[name="role"]').selectOption("owner");
    await card().getByRole("button", { name: "Add" }).click();

    await expect(page.locator("#orgs")).toContainText("orgowner");
    expect(orgMemberRole("acme", "orgowner")).toBe("owner");
    await expect(card().locator("sl-badge", { hasText: "owner" })).toBeVisible();

    // The only owner can be neither demoted nor removed.
    await card().getByRole("button", { name: "Make member" }).click();
    await expect(page.locator("#flash")).toContainText("only owner");
    expect(orgMemberRole("acme", "orgowner")).toBe("owner");

    await card().getByRole("button", { name: "Remove" }).click();
    await expect(page.locator("#flash")).toContainText("only owner");
    expect(orgMemberCount("acme")).toBe(1);

    // A second owner lifts the protection from the first.
    seedSignedInUser(4311, "orgsecond");
    await card().locator('form[action="/admin/orgs/members/add"] input[name="login"]').fill("orgsecond");
    await card().locator('form[action="/admin/orgs/members/add"] select[name="role"]').selectOption("owner");
    await card().getByRole("button", { name: "Add" }).click();
    await expect(page.locator("#orgs")).toContainText("orgsecond");

    await card()
      .locator("tr", { hasText: "orgowner" })
      .getByRole("button", { name: "Remove" })
      .click();
    await expect(page.locator("#orgs")).not.toContainText("orgowner");
    expect(orgMemberRole("acme", "orgsecond")).toBe("owner");

    await card().getByRole("button", { name: "Delete" }).click();
    await expect(page.locator("#orgs")).not.toContainText("@acme");
  });

  test("a malformed slug is rejected with a flash", async ({ page }) => {
    await page.locator('form[action="/admin/orgs/create"] input[name="slug"]').fill("Acme Corp");
    await page.getByRole("button", { name: "Create organisation" }).click();
    await expect(page.locator("#flash")).toContainText("lowercase");
    expect(orgCount()).toBe(0);
  });
});
