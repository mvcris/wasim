# Architecture

wasim plays both sides of Meta for one business account.

```text
                         +-----------------------------------------+
                         |                 wasim                   |
 your app                |                                         |
 POST /messages -------->| graphapi  --+                           |
 GET  /{media-id}  ----->|             |                           |
 GET  /message_templates>|             v                           |
                         |            sim  ---> store (SQLite)     |
 POST /webhook  <--------| webhook <-- outbox                      |
 (signed)                |                                         |
                         | console  --> sim      <---- browser     |
                         +-----------------------------------------+
```

## Packages

| Package | Responsibility |
|---|---|
| `internal/cloudapi` | The wire contract: request shapes, limits, error catalog, template rules, wamids. No I/O. |
| `internal/webhook` | Webhook payloads, canonical encoding, signing, the dispatcher, the verification handshake. |
| `internal/store` | SQLite access and embedded migrations. |
| `internal/domain` | Persisted entities and the status progression. |
| `internal/sim` | The engine: what happens to a message and which webhooks result. |
| `internal/graphapi` | The HTTP API the consumer talks to. |
| `internal/console` | The control API and the embedded browser UI. |
| `internal/events` | The change signal behind the console's live updates. |
| `internal/app` | Wires everything into a process. Its tests run the whole process against a fake consumer. |
| `internal/doccheck` | No code: its test enforces the godoc conventions. |
| `cmd/wasim` | The command: flags, signals, exit codes. |
| `e2e` | Playwright tests that drive the console in a real browser, and the README screenshots. |

## Decisions

**Two ports.** The Graph API faces the consumer and the console faces a human.
The console has no authentication, so it must be possible to expose one without
the other.

**Webhooks go through a persistent outbox.** A request writes its message and its
webhooks in one transaction, and the dispatcher delivers them independently. That
gives scheduled statuses without timers, survives restarts, and means delivery
never holds anything a Graph request needs. The last point is not theoretical:
consumers commonly download media from inside their webhook handler, which calls
back into wasim during the delivery.

**A status changes when its webhook goes out, not when it is scheduled.** The
console then shows the same timeline the consumer experiences.

**Bytes are signed once and stored.** A webhook body is serialized when enqueued,
and the same bytes are signed and sent on every attempt, which is what a raw-body
signature check requires. Redelivery reuses them to produce true duplicates.

**One database connection.** SQLite allows one writer anyway, and `:memory:` would
otherwise be a separate database per connection. Code inside a transaction must use
the `Queries` it receives, or it waits forever for the connection it already holds.

**Failures are data.** Injected failures are rows consumed by sends, so a scenario
can be set up from the console or from a test through the same API.
