import { ana, graphVersion } from "../support/env";
import { businessBubbles, expect, openConversation, test, userBubbles } from "../support/fixtures";

// A 1x1 transparent PNG.
const tinyPNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
  "base64",
);

test("a message typed as the user reaches the app as a signed text webhook", async ({ page, harness }) => {
  await openConversation(page, ana.name);
  await page.getByLabel("Message text").fill("Is my order on its way?");
  await page.getByRole("button", { name: "Send", exact: true }).click();

  await expect(userBubbles(page).last()).toContainText("Is my order on its way?");
  const message = await harness.waitForInbound((item) => item.type === "text");
  expect(message.text?.body).toBe("Is my order on its way?");
});

test("a text the app sends appears and advances to delivered", async ({ page, harness }) => {
  await harness.userSays(ana.waID, "Hello");
  await openConversation(page, ana.name);
  await harness.sendFromAppOK({ to: ana.waID, type: "text", text: { body: "It ships today." } });

  const bubble = businessBubbles(page).last();
  await expect(bubble).toContainText("It ships today.");
  await expect(bubble.locator(".bubble-meta > .badge")).toHaveText("delivered");
});

test("an attached image reaches the app as media it can download", async ({ page, harness }) => {
  await openConversation(page, ana.name);
  await page.locator("#attachment").setInputFiles({ name: "receipt.png", mimeType: "image/png", buffer: tinyPNG });
  await page.getByLabel("Message text").fill("Here is the receipt");
  await page.getByRole("button", { name: "Send", exact: true }).click();

  const message = await harness.waitForInbound((item) => item.type === "image");
  const lookup = await (await harness.graph(`/${graphVersion}/${message.image?.id}`)).json();
  const download = await harness.graph(lookup.url);
  expect(Buffer.from(await download.body()).equals(tinyPNG)).toBe(true);
});
