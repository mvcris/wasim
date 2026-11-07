import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createWriteStream, mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { startConsumer } from "./support/consumer";
import { appSecret, consoleURL, consumerURL, graphURL, verifyToken, webhookURL } from "./support/env";

const repoRoot = path.resolve(import.meta.dirname, "..");
const startupTimeoutMs = 15_000;

function port(url: string): number {
  return Number(new URL(url).port);
}

async function waitUntilUp(url: string, wasim: ChildProcess, logPath: string): Promise<void> {
  const deadline = Date.now() + startupTimeoutMs;
  while (Date.now() < deadline) {
    if (wasim.exitCode !== null) {
      throw new Error(`wasim exited with code ${wasim.exitCode}, see ${logPath}`);
    }
    try {
      if ((await fetch(url)).ok) return;
    } catch {
      // Not listening yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`wasim did not answer ${url} within ${startupTimeoutMs} ms, see ${logPath}`);
}

// Builds wasim from this checkout and runs it against an in-process consumer.
// State lives in memory, so every run starts from the built-in seed.
export default async function globalSetup(): Promise<() => Promise<void>> {
  const workDir = mkdtempSync(path.join(tmpdir(), "wasim-e2e-"));
  const binary = path.join(workDir, "wasim");
  execFileSync("go", ["build", "-o", binary, "./cmd/wasim"], { cwd: repoRoot, stdio: "inherit" });
  // Workers inherit the environment, so a spec that needs its own instance,
  // such as one with console users, can start it from the same build.
  process.env.WASIM_E2E_BINARY = binary;

  const consumer = await startConsumer(port(consumerURL));

  const logDir = path.join(import.meta.dirname, "test-results");
  mkdirSync(logDir, { recursive: true });
  const logPath = path.join(logDir, "wasim.log");
  const log = createWriteStream(logPath);
  const wasim = spawn(binary, [], {
    env: {
      ...process.env,
      WASIM_GRAPH_ADDR: new URL(graphURL).host,
      WASIM_CONSOLE_ADDR: new URL(consoleURL).host,
      WASIM_PUBLIC_URL: graphURL,
      WASIM_WEBHOOK_URL: webhookURL,
      WASIM_APP_SECRET: appSecret,
      WASIM_VERIFY_TOKEN: verifyToken,
      WASIM_DB_PATH: ":memory:",
      WASIM_MEDIA_DIR: path.join(workDir, "media"),
      WASIM_STATUS_DELAY: "50ms",
      WASIM_WEBHOOK_ATTEMPTS: "2",
      WASIM_WEBHOOK_RETRY_BASE: "100ms",
      WASIM_LOG_LEVEL: "debug",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  wasim.stdout?.pipe(log);
  wasim.stderr?.pipe(log);

  await waitUntilUp(`${consoleURL}/api/info`, wasim, logPath);

  return async () => {
    wasim.kill("SIGTERM");
    await new Promise((resolve) => (wasim.exitCode !== null ? resolve(null) : wasim.once("exit", resolve)));
    await new Promise((resolve) => consumer.close(resolve));
    rmSync(workDir, { recursive: true, force: true });
  };
}
