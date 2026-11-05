import { ana, consoleURL } from "../support/env";
import { businessBubbles, expect, openConversation, test } from "../support/fixtures";

test("Expire window closes the 24h window badge", async ({ page, harness }) => {
  await harness.userSays(ana.waID, "Hi");
  await openConversation(page, ana.name);
  await expect(page.locator("#chat-header")).toContainText("24h window open");

  await page.getByRole("button", { name: "Expire window" }).click();
  await expect(page.locator("#chat-header")).toContainText("24h window closed");
});

test("a free-form send after the window expires fails with 131047", async ({ page, harness }) => {
  await harness.userSays(ana.waID, "Hi");
  await openConversation(page, ana.name);
  await page.getByRole("button", { name: "Expire window" }).click();
  await expect(page.locator("#chat-header")).toContainText("24h window closed");

  await harness.sendFromAppOK({ to: ana.waID, type: "text", text: { body: "Are you still there?" } });
  await expect(businessBubbles(page).last().locator(".bubble-error")).toContainText("(#131047)");
});

test("the window failure reaches the app as a failed status webhook", async ({ harness }) => {
  const messageID = await harness.sendFromAppOK({ to: ana.waID, type: "text", text: { body: "Hello?" } });
  await expect
    .poll(async () => (await harness.statuses()).find((status) => status.id === messageID && status.status === "failed")?.errors?.[0]?.code)
    .toBe(131047);
});

test("a sync failure rule makes the next send answer an HTTP error", async ({ page, harness }) => {
  await harness.userSays(ana.waID, "Hi");
  await openConversation(page, ana.name);
  await page.getByRole("tab", { name: "Failures" }).click();
  await page.getByLabel("Error", { exact: true }).selectOption("131056");
  await page.getByLabel("Arrives as", { exact: true }).selectOption("sync");
  await page.getByRole("button", { name: "Add rule" }).click();
  await expect(page.locator("#rule-list")).toContainText("#131056");
  await expect(page.getByLabel("Arrives as", { exact: true })).toHaveValue("");

  const response = await harness.sendFromApp({ to: ana.waID, type: "text", text: { body: "Hello" } });
  expect({ status: response.status(), code: (await response.json()).error.code }).toEqual({ status: 400, code: 131056 });
});

test("a webhook resent with a bad signature is rejected by the app", async ({ page, harness, request }) => {
  await harness.userSays(ana.waID, "Hi");
  await harness.waitForInbound((item) => item.type === "text");
  await openConversation(page, ana.name);

  const delivery = page.locator("#delivery-list .delivery").filter({ hasText: "message text" });
  await delivery.getByRole("button", { name: /message text/ }).click();
  await delivery.getByRole("button", { name: "Send with bad signature" }).click();

  await expect
    .poll(async () => {
      const deliveries = await (await request.get(`${consoleURL}/api/deliveries`)).json();
      return deliveries.find((item: { bad_signature?: boolean }) => item.bad_signature)?.last_status;
    })
    .toBe(401);
  await expect(page.locator("#delivery-list")).toContainText("last HTTP 401, bad signature");
});
