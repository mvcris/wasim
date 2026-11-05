import path from "node:path";

import type { APIRequestContext } from "@playwright/test";

import { ana, bruno, consoleURL } from "../support/env";
import { businessBubbles, expect, openConversation, test, type Harness } from "../support/fixtures";

// Regenerates the README images: `pnpm --dir e2e screenshots`.

const imagesDir = path.resolve(import.meta.dirname, "../../docs/images");
const shot = (name: string) => path.join(imagesDir, name);

async function buildConversation(harness: Harness, request: APIRequestContext) {
  const priya = { waID: "15550000003", name: "Priya Shah" };
  await request.post(`${consoleURL}/api/contacts`, { data: { wa_id: priya.waID, name: priya.name } });
  await harness.userSays(priya.waID, "Do you ship to Canada?");
  await harness.userSays(bruno.waID, "Can I change my delivery address?");

  await harness.userSays(ana.waID, "Hi! I ordered running shoes yesterday. Any news on the delivery?");
  const shipped = await harness.sendFromAppOK({
    to: ana.waID,
    type: "text",
    text: { body: "Hi Ana! Good news: your order shipped this morning." },
  });
  await harness.sendFromAppOK({
    to: ana.waID,
    type: "template",
    template: {
      name: "order_confirmation",
      language: { code: "en_US" },
      components: [{ type: "body", parameters: [{ type: "text", text: "Ana" }, { type: "text", text: "SKU-4821" }] }],
    },
  });
  return { shipped };
}

test("console screenshots", async ({ page, harness, request }) => {
  const { shipped } = await buildConversation(harness, request);
  await expect.poll(async () => (await harness.statuses()).filter((status) => status.status === "delivered").length).toBe(2);
  await request.post(`${consoleURL}/api/messages/${encodeURIComponent(shipped)}/status`, { data: { status: "read" } });

  await openConversation(page, ana.name);
  await businessBubbles(page).last().getByRole("button", { name: "Track order" }).click();
  await harness.waitForInbound((message) => message.type === "button");

  await request.post(`${consoleURL}/api/contacts/${ana.waID}/messages`, {
    data: { type: "text", text: "Great, thanks! Can I pick a delivery time?", context_id: shipped },
  });
  await harness.sendFromAppOK({
    to: ana.waID,
    type: "interactive",
    interactive: {
      type: "list",
      header: { type: "text", text: "Delivery time" },
      body: { text: "Sure! Pick a window for tomorrow." },
      action: {
        button: "See windows",
        sections: [
          {
            title: "Morning",
            rows: [
              { id: "am-early", title: "8 to 10 am" },
              { id: "am-late", title: "10 am to noon" },
            ],
          },
          {
            title: "Afternoon",
            rows: [
              { id: "pm-early", title: "1 to 3 pm", description: "Most popular" },
              { id: "pm-late", title: "3 to 5 pm" },
            ],
          },
        ],
      },
    },
  });
  const list = businessBubbles(page).filter({ hasText: "Delivery time" });
  await expect(list.locator(".bubble-meta > .badge")).toHaveText("delivered");
  await expect.poll(async () => (await request.get(`${consoleURL}/api/deliveries`)).json().then((items) => items.filter((item: { state: string }) => item.state !== "delivered").length)).toBe(0);
  await list.getByRole("button", { name: "See windows" }).click();
  await expect(list.getByRole("button", { name: /1 to 3 pm/ })).toBeVisible();
  // Start the view at the template so its label is not cut in half.
  await businessBubbles(page).filter({ hasText: "SKU-4821" }).evaluate((node) => node.scrollIntoView({ block: "start" }));
  await page.mouse.move(0, 0);
  await page.screenshot({ path: shot("console-chat.png") });

  const delivery = page.locator("#delivery-list .delivery").filter({ hasText: "message text" }).first();
  await delivery.locator(".delivery-head").click();
  await expect(delivery.locator("pre")).toBeVisible();
  await page.mouse.move(0, 0);
  await page.screenshot({ path: shot("console-webhooks.png") });

  await page.getByRole("tab", { name: "Failures" }).click();
  await page.getByLabel("Applies to", { exact: true }).selectOption("all");
  await page.getByLabel("Error", { exact: true }).selectOption("131049");
  await page.getByLabel("How many sends", { exact: true }).fill("-1");
  await page.getByRole("button", { name: "Add rule" }).click();
  await page.getByLabel("Applies to", { exact: true }).selectOption("contact");
  await page.getByLabel("Error", { exact: true }).selectOption("131056");
  await page.getByLabel("Arrives as", { exact: true }).selectOption("sync");
  await page.getByLabel("How many sends", { exact: true }).fill("2");
  await page.getByRole("button", { name: "Add rule" }).click();
  await expect(page.locator("#rule-list .rule")).toHaveCount(2);
  await page.mouse.move(0, 0);
  await page.screenshot({ path: shot("console-failures.png") });
});
