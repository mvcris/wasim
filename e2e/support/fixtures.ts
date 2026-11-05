import { expect, test as base, type APIRequestContext, type Page } from "@playwright/test";

import { accessToken, consoleURL, consumerURL, graphURL, graphVersion, phoneNumberID } from "./env";

// Webhook envelope, reduced to the parts the tests read.
export type Envelope = {
  entry: { changes: { value: { messages?: InboundMessage[]; statuses?: Status[] } }[] }[];
};

export type InboundMessage = {
  id: string;
  from: string;
  type: string;
  context?: { from: string; id: string };
  text?: { body: string };
  image?: { id: string; mime_type: string; sha256: string };
  interactive?: { type: string; button_reply?: { id: string; title: string }; list_reply?: { id: string; title: string } };
  button?: { payload: string; text: string };
};

export type Status = {
  id: string;
  status: string;
  errors?: { code: number }[];
};

type Received = { accepted: Envelope[]; rejected: number };

export class Harness {
  constructor(private readonly request: APIRequestContext) {}

  async reset(): Promise<void> {
    expect((await this.request.post(`${consoleURL}/api/reset`)).ok()).toBe(true);
    expect((await this.request.post(`${consumerURL}/clear`)).ok()).toBe(true);
  }

  async received(): Promise<Received> {
    return (await this.request.get(`${consumerURL}/received`)).json();
  }

  async inboundMessages(): Promise<InboundMessage[]> {
    const { accepted } = await this.received();
    return accepted.flatMap((envelope) => envelope.entry[0].changes[0].value.messages ?? []);
  }

  async statuses(): Promise<Status[]> {
    const { accepted } = await this.received();
    return accepted.flatMap((envelope) => envelope.entry[0].changes[0].value.statuses ?? []);
  }

  // Waits for the consumer to accept an inbound message matching the predicate.
  async waitForInbound(predicate: (message: InboundMessage) => boolean): Promise<InboundMessage> {
    let found: InboundMessage | undefined;
    await expect
      .poll(async () => {
        found = (await this.inboundMessages()).find(predicate);
        return found !== undefined;
      })
      .toBe(true);
    return found as InboundMessage;
  }

  // Sends from the app through the fake Graph API, like a real integration.
  async sendFromApp(body: Record<string, unknown>) {
    return this.request.post(`${graphURL}/${graphVersion}/${phoneNumberID}/messages`, {
      headers: { Authorization: `Bearer ${accessToken}` },
      data: { messaging_product: "whatsapp", ...body },
    });
  }

  async sendFromAppOK(body: Record<string, unknown>): Promise<string> {
    const response = await this.sendFromApp(body);
    expect(response.status(), await response.text()).toBe(200);
    return (await response.json()).messages[0].id;
  }

  // Plays a user message through the console API. Tests that are not about the
  // composer use it to open the 24h window.
  async userSays(waID: string, text: string): Promise<string> {
    const response = await this.request.post(`${consoleURL}/api/contacts/${waID}/messages`, { data: { type: "text", text } });
    expect(response.status()).toBe(201);
    return (await response.json()).id;
  }

  graph(path: string) {
    return this.request.get(path.startsWith("http") ? path : `${graphURL}${path}`, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
  }
}

// Every test starts from the seed: the reset runs even when a test does not
// ask for the harness.
export const test = base.extend<{ harness: Harness }>({
  harness: [
    async ({ request }, use) => {
      const harness = new Harness(request);
      await harness.reset();
      await use(harness);
    },
    { auto: true },
  ],
});

export { expect };

// Opens the console with a user's conversation selected.
export async function openConversation(page: Page, name: string): Promise<void> {
  await page.goto("/");
  await expect(page.locator("#connection")).toHaveText("live");
  await page.getByRole("complementary", { name: "Contacts" }).getByRole("button", { name: new RegExp(name) }).click();
  await expect(page.locator("#chat-header")).toContainText(name);
}

export const businessBubbles = (page: Page) => page.locator(".bubble.business");
export const userBubbles = (page: Page) => page.locator(".bubble.user");
