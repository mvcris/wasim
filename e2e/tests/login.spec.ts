import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { expect, test } from "@playwright/test";

import { ana } from "../support/env";

// The shared instance has no users, so its console is open. These tests run a
// second instance, on its own ports and database file, with one user added
// through the command line as an operator would.
const consoleURL = "http://127.0.0.1:17171";
const graphAddr = "127.0.0.1:17170";
const email = "e2e@example.com";
const password = "e2e password";
const basicAuth = { Authorization: `Basic ${Buffer.from(`${email}:${password}`).toString("base64")}` };

let wasim: ChildProcess | undefined;
let workDir = "";

test.use({ baseURL: consoleURL });

test.beforeAll(async () => {
  const binary = process.env.WASIM_E2E_BINARY;
  if (!binary) throw new Error("WASIM_E2E_BINARY is not set: run the tests through the global setup");
  workDir = mkdtempSync(path.join(tmpdir(), "wasim-e2e-login-"));
  const env = {
    ...process.env,
    WASIM_GRAPH_ADDR: graphAddr,
    WASIM_CONSOLE_ADDR: new URL(consoleURL).host,
    WASIM_DB_PATH: path.join(workDir, "wasim.db"),
    WASIM_MEDIA_DIR: path.join(workDir, "media"),
  };
  execFileSync(binary, ["user", "add", email, "--password-stdin"], { env, input: `${password}\n` });
  wasim = spawn(binary, [], { env, stdio: "ignore" });

  await expect
    .poll(async () => {
      try {
        return (await fetch(`${consoleURL}/login`)).status;
      } catch {
        return 0;
      }
    })
    .toBe(200);
});

test.afterAll(async () => {
  if (wasim && wasim.exitCode === null) {
    const exited = new Promise((resolve) => wasim?.once("exit", resolve));
    wasim.kill("SIGTERM");
    await exited;
  }
  rmSync(workDir, { recursive: true, force: true });
});

test("the console asks for a login, then lets the user in and out", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(`${consoleURL}/login`);

  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill("wrong password");
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("alert")).toHaveText("wrong email or password");

  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(`${consoleURL}/`);
  await expect(page.getByRole("complementary", { name: "Contacts" }).getByRole("button", { name: new RegExp(ana.name) })).toBeVisible();
  await expect(page.getByText(email, { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page).toHaveURL(`${consoleURL}/login`);
  await page.goto("/");
  await expect(page).toHaveURL(`${consoleURL}/login`);
});

test("scripts reach the console API with Basic credentials", async ({ request }) => {
  expect((await request.get("/api/contacts")).status()).toBe(401);
  const contacts = await request.get("/api/contacts", { headers: basicAuth });
  expect(contacts.status()).toBe(200);
  expect(await contacts.json()).toContainEqual(expect.objectContaining({ wa_id: ana.waID }));
});
