import { ana } from "../support/env";
import { businessBubbles, expect, openConversation, test } from "../support/fixtures";

test("tapping a reply button sends a button_reply webhook with its id", async ({ page, harness }) => {
  await harness.userSays(ana.waID, "I need to reschedule");
  await openConversation(page, ana.name);
  await harness.sendFromAppOK({
    to: ana.waID,
    type: "interactive",
    interactive: {
      type: "button",
      body: { text: "What would you like to do?" },
      action: {
        buttons: [
          { type: "reply", reply: { id: "change-slot", title: "Change" } },
          { type: "reply", reply: { id: "cancel-slot", title: "Cancel" } },
        ],
      },
    },
  });

  await businessBubbles(page).last().getByRole("button", { name: "Change" }).click();
  const message = await harness.waitForInbound((item) => item.type === "interactive");
  expect(message.interactive?.button_reply).toEqual({ id: "change-slot", title: "Change" });
});

test("picking a list row sends a list_reply webhook", async ({ page, harness }) => {
  await harness.userSays(ana.waID, "Which shipping options are there?");
  await openConversation(page, ana.name);
  await harness.sendFromAppOK({
    to: ana.waID,
    type: "interactive",
    interactive: {
      type: "list",
      body: { text: "Choose a shipping option" },
      action: {
        button: "Shipping options",
        sections: [
          {
            rows: [
              { id: "express", title: "Express", description: "Next day" },
              { id: "standard", title: "Standard" },
            ],
          },
        ],
      },
    },
  });

  const bubble = businessBubbles(page).last();
  await bubble.getByRole("button", { name: "Shipping options" }).click();
  await bubble.getByRole("button", { name: /Express/ }).click();
  const message = await harness.waitForInbound((item) => item.interactive?.type === "list_reply");
  expect(message.interactive?.list_reply?.id).toBe("express");
});

test("a template quick reply tap sends its label as the button payload", async ({ page, harness }) => {
  await openConversation(page, ana.name);
  await harness.sendFromAppOK({
    to: ana.waID,
    type: "template",
    template: {
      name: "order_confirmation",
      language: { code: "en_US" },
      components: [{ type: "body", parameters: [{ type: "text", text: "Ana" }, { type: "text", text: "SKU-42" }] }],
    },
  });

  await businessBubbles(page).last().getByRole("button", { name: "Track order" }).click();
  const message = await harness.waitForInbound((item) => item.type === "button");
  expect(message.button).toEqual({ payload: "Track order", text: "Track order" });
});
