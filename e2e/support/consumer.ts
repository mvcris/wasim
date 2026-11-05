import { createHmac, timingSafeEqual } from "node:crypto";
import { createServer, type Server } from "node:http";

import { appSecret, verifyToken } from "./env";

// A stand-in for the app under test. It does what a careful integration does:
// answers the verification handshake, checks X-Hub-Signature-256 against the
// raw body bytes, and only then accepts the webhook. Tests read what it
// received over HTTP because they run in separate worker processes.

type Received = {
  accepted: unknown[];
  rejected: number;
};

function signatureMatches(body: Buffer, header: string | undefined): boolean {
  if (!header?.startsWith("sha256=")) return false;
  const expected = Buffer.from(`sha256=${createHmac("sha256", appSecret).update(body).digest("hex")}`);
  const actual = Buffer.from(header);
  return expected.length === actual.length && timingSafeEqual(expected, actual);
}

export function startConsumer(port: number): Promise<Server> {
  const received: Received = { accepted: [], rejected: 0 };

  const server = createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://consumer");

    if (request.method === "GET" && url.pathname === "/webhook") {
      const ok = url.searchParams.get("hub.mode") === "subscribe" && url.searchParams.get("hub.verify_token") === verifyToken;
      response.writeHead(ok ? 200 : 403).end(ok ? (url.searchParams.get("hub.challenge") ?? "") : "");
      return;
    }

    if (request.method === "POST" && url.pathname === "/webhook") {
      const chunks: Buffer[] = [];
      request.on("data", (chunk: Buffer) => chunks.push(chunk));
      request.on("end", () => {
        const body = Buffer.concat(chunks);
        if (!signatureMatches(body, request.headers["x-hub-signature-256"] as string | undefined)) {
          received.rejected += 1;
          response.writeHead(401).end();
          return;
        }
        received.accepted.push(JSON.parse(body.toString("utf8")));
        response.writeHead(200).end();
      });
      return;
    }

    if (request.method === "GET" && url.pathname === "/received") {
      response.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(received));
      return;
    }

    if (request.method === "POST" && url.pathname === "/clear") {
      received.accepted = [];
      received.rejected = 0;
      response.writeHead(204).end();
      return;
    }

    response.writeHead(404).end();
  });

  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "127.0.0.1", () => resolve(server));
  });
}
