import { ana, bruno, graphURL, phoneNumberID } from "../support/env";
import { expect, openConversation, test } from "../support/fixtures";

test("the console loads with the seeded users", async ({ page}) => {
  await page.goto("/");
  const contacts = page.getByRole("complementary", { name: "Contacts" });
  await expect(contacts.getByRole("button", { name: new RegExp(ana.name) })).toBeVisible();
  await expect(contacts.getByRole("button", { name: new RegExp(bruno.name) })).toBeVisible();
});

test("the Setup tab shows the values to configure in the app", async ({ page}) => {
  await page.goto("/");
  await page.getByRole("tab", { name: "Setup" }).click();
  const values = page.locator("#setup-values");
  await expect(values).toContainText(`Graph API base URL   ${graphURL}`);
  await expect(values).toContainText(`Phone number id      ${phoneNumberID}`);
});

test("adding a user from the form selects them", async ({ page}) => {
  await page.goto("/");
  await page.getByLabel("Phone number", { exact: true }).fill("+1 555 000 0042");
  await page.getByLabel("Profile name", { exact: true }).fill("Carla Mendes");
  await page.getByRole("button", { name: "Add user" }).click();
  await expect(page.locator("#chat-header")).toContainText("Carla Mendes");
  await expect(page.locator("#chat-header")).toContainText("+15550000042");
});

test("Verify webhook reports that the app echoed the challenge", async ({ page}) => {
  await openConversation(page, ana.name);
  await page.getByRole("button", { name: "Verify webhook" }).click();
  await expect(page.getByRole("status")).toHaveText("Webhook verified: your app echoed the challenge");
});
