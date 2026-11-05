# Errors

Meta's error reference states that errors arrive "synchronously, asynchronously, or
sometimes through both", and no longer lists an HTTP status per code. wasim therefore
treats the channel as configuration: every failure can take either one, and each code
has a default.

| Code | Meaning | Default channel | HTTP status when sync |
|---|---|---|---|
| 131047 | More than 24 hours since the user last replied | async | 400 |
| 131026 | Message undeliverable | async | 400 |
| 131049 | Withheld to maintain ecosystem engagement | async | 400 |
| 130472 | Number is part of an experiment | async | 400 |
| 132000 | Template parameter count or format mismatch | sync | 400 |
| 132001 | Template missing in this language, or not approved | sync | 404 |
| 131051 | Unsupported message type | sync | 400 |
| 131056 | Pair rate limit hit | sync | 400 |
| 130429 | Throughput limit hit | sync | 400 |
| 131030 | Recipient not in the test number's allowed list | sync | 400 |
| 100 | Invalid parameter | sync | 400 |
| 190 | Invalid or expired access token | sync | 401 |
| 131008 | Required parameter missing | sync | 400 |
| 131009 | Invalid parameter value | sync | 400 |
| 131021 | Sender and recipient are the same | sync | 400 |
| 1 | Unknown error | sync | 500 |

## Where the sources disagree

These are the emulator's choices, not facts about Meta. Corrections backed by real
traffic are welcome.

| Topic | Documentation or source A | Source B | wasim |
|---|---|---|---|
| 131047 channel | An archived Meta table lists HTTP 400 | Production consumers report only a failed webhook | Async, configurable with `WASIM_WINDOW_ERROR_CHANNEL` |
| 130429 status | An archived Meta table lists HTTP 400 | Another emulator's captured traffic shows HTTP 429 | 400 |
| Wrong parameter grammar on a template | The reference suggests 132012 | Production consumers report 132000 | 132000 |
| Unknown object id | The template reference shows HTTP 404, code 803 | Meta's OpenAPI example and captured traffic show HTTP 400, code 100, subcode 33 | 400, code 100, subcode 33 |
| `recipient_type` | Documented as required | Production consumers omit it and succeed | Optional |
| `message_status` | Documented for paced templates only | Seen as `accepted` on normal sends | `accepted` on template sends |
| Non-ASCII in webhook bodies | Documented as `\uXXXX` for Messenger only | Asserted for WhatsApp by captured traffic | Escaped by default, configurable with `WASIM_ESCAPE_UNICODE` |
| Quick-reply `payload` | Equal to the label unless overridden at send time | | Same |

A short capture session against a real test number would settle most of these.
