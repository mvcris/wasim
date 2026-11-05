# Compatibility with the WhatsApp Cloud API

This page is the honest answer to "will my integration behave the same against
Meta?". It lists what wasim emulates, what it does not, and every place where
it knowingly behaves differently from the real platform.

Facts about Meta come from [the research notes](research/cloud-api-basics.md),
which cite a source and a confidence level for each claim. Where the sources
disagree, the choice wasim made is recorded in [errors.md](errors.md#where-the-sources-disagree).

Legend:

| Mark | Meaning |
|---|---|
| Yes | Emulated and covered by tests |
| Partial | Emulated with the limits listed next to it |
| No | Not emulated. The request fails or the feature is absent |

## Graph API endpoints

| Endpoint | Status | Notes |
|---|---|---|
| `POST /{version}/{phone-number-id}/messages` (send) | Partial | Text, reply buttons, lists and templates. See [message types](#sending) |
| `POST /{version}/{phone-number-id}/messages` (mark as read) | Yes | Marks the message and every earlier user message as read. `typing_indicator` is accepted and ignored |
| `GET /{version}/{media-id}` | Yes | `url`, `mime_type`, `sha256` (base64), `file_size` (string), `id` |
| `GET` of the media URL | Yes | Bearer token required, URL expires after 5 minutes, real `Content-Type`, `Range` requests |
| `GET /{version}/{waba-id}/message_templates` | Partial | `fields`, `limit`, `after`, `before`, `name`, `status`, absolute `paging.next` and `paging.previous`. Only 7 fields exist, see [drifts](#templates) |
| `POST /{version}/{phone-number-id}/media` (upload) | No | |
| `DELETE /{version}/{media-id}` | No | |
| Template create, edit and delete | No | Templates come from the seed file |
| Phone number, business profile, registration, two-step verification | No | `GET /{version}/{phone-number-id}` answers "unknown object" |
| Flows, Groups, Calling, Payments, Marketing Messages API | No | |

Every route needs a bearer token (header or `access_token` query parameter).
With `WASIM_ACCESS_TOKENS` empty, any non-empty token is accepted.

## Sending

| Message type | Status | Notes |
|---|---|---|
| `text` | Yes | `body` up to 4096 characters, `preview_url` accepted and not rendered |
| `interactive` / `button` | Yes | 1 to 3 reply buttons, unique ids and titles, every documented length limit. Media headers pass validation and show as a label |
| `interactive` / `list` | Yes | Up to 10 sections and 10 rows in total, section titles required when there is more than one, every documented length limit |
| `template` | Partial | Positional and named body and header parameters, `currency` and `date_time` fallbacks, quick-reply payload override, `components: []`. Media headers, carousels, limited time offers, copy code and URL button parameters are not validated |
| `context` (reply to a message) | Yes | Stored and shown as a quote in the console |
| `recipient_type` | Partial | Optional, as production consumers send it. Only `individual` is accepted |
| `recipient` (BSUID) | No | `to` is required |
| `image`, `document`, `audio`, `video`, `sticker` | No | 131051 |
| `location`, `contacts`, `reaction` | No | 131051 |
| `interactive` CTA URL, flow, product, catalog, location request | No | 131051 |

The success response has `messaging_product`, `contacts[0].input` exactly as
sent, `contacts[0].wa_id` as digits, and `messages[0].id`. Template sends also
carry `messages[0].message_status: "accepted"`.

## What a simulated user can do

| Action | Webhook | Status |
|---|---|---|
| Send text | `messages[].type: text` | Yes |
| Send an image (JPEG, PNG, WebP up to 5 MB) | `type: image` with `id`, `sha256`, `mime_type`, `url`, `caption` | Yes |
| Send a document (the 8 documented MIME types up to 100 MB) | `type: document` with `filename` | Yes |
| Tap a reply button | `interactive.button_reply` | Yes |
| Pick a list row | `interactive.list_reply` with `description` | Yes |
| Tap a template quick reply | `type: button` with `payload` and `text` | Yes |
| Quote a message | `context.from` and `context.id` | Yes |
| Write a back-dated message | `timestamp` in the past, window stays closed | Yes |
| Audio, video, sticker, location, contacts, reaction | | No |
| Edit or delete a message (`unsupported`) | | No |
| Forwarded messages, ads referrals, "Message business" button | | No |
| Block the business, opt out of marketing | | No |

## Statuses and delivery

| Behaviour | Status | Notes |
|---|---|---|
| `sent`, `delivered`, `read`, `failed` status webhooks | Yes | Automatic (`WASIM_AUTO_STATUSES`) or on demand from the console |
| `failed` with the webhook error object (`code`, `title`, `message`, `error_data.details`, `href`) | Yes | |
| `pricing` on `sent` and `delivered` | Partial | See [pricing](#statuses-and-pricing) |
| Status only moves forward in the console | Yes | Out of order webhooks do not undo progress |
| `X-Hub-Signature-256` over the exact bytes sent | Yes | HMAC-SHA256, lowercase hex, `sha256=` prefix |
| Non-ASCII escaped as `\uXXXX` | Yes | On by default, `WASIM_ESCAPE_UNICODE=false` sends raw UTF-8 |
| Only HTTP 200 acknowledges | Yes | 201, 204 and redirects count as failures, as documented |
| Retries with exponential backoff | Partial | See [delivery](#webhook-delivery) |
| Concurrent delivery, no ordering guarantee | Yes | `WASIM_WEBHOOK_WORKERS` |
| Duplicates and bad signatures on demand | Yes | Console or `POST /api/deliveries/{id}/redeliver` |
| Verification handshake (`hub.mode`, `hub.challenge`, `hub.verify_token`) | Yes | On demand or at startup |
| `value.errors` (system and account errors) | No | |
| `played` status, multi-device `delivered` plus `failed` | No | Can be approximated with manual statuses |
| Template status, quality and account webhooks | No | |

## 24 hour customer service window

| Behaviour | Status |
|---|---|
| A user message opens or renews the window for 24 hours | Yes |
| Free-form messages (text, interactive) fail outside it with 131047 | Yes |
| Templates go out with the window closed | Yes |
| The failure channel is configurable (`async` by default, or `sync`) | Yes |
| Expire the window on demand | Yes |
| Free entry point windows (ads, "Message business" button) | No |

## Errors

16 error codes can be injected through either channel. The catalog, the
default channel of each code and the reasoning are in [errors.md](errors.md).
Validation failures, unknown templates, wrong parameter counts, bad tokens and
unknown object ids raise the matching code without any injection.

## Known drifts

These are the places where a consumer can pass against wasim and still behave
differently in production. Each one is deliberate, and most are cheap to close
once someone needs them. Contributions with captured real traffic are the
fastest way to fix them.

### Identity and phone numbers

- **No business-scoped user ids.** Since April 2026 Meta adds `user_id`,
  `from_user_id` and `recipient_user_id` to webhooks, a `contacts` block to
  status webhooks, and can omit `wa_id` and `from` entirely for users who
  adopted a username. wasim sends none of this, so code that keys users only
  by phone number is not challenged.
- **Numbers are never rewritten.** For Brazil and Mexico Meta "may modify the
  extra added prefix" of a number, so `contacts[].wa_id` can differ from the
  `to` you sent (the Brazilian ninth digit is the classic case). wasim always
  answers with the digits it received, so a consumer that matches users by
  exact number will not see the duplicate it would get in production.
- **No country code is inferred.** Meta prepends the business number's country
  code to a `to` without a leading `+`. wasim treats the digits as complete.

### Webhook shape

- **One version shape for everything.** wasim accepts any `/vMAJOR.MINOR`
  prefix, including expired and nonexistent versions, and always emits the
  v24.0+ status shape (no `conversation` object). An app pinned to an older
  version receives `conversation` in production.
- **One update per POST.** Meta may batch up to 1000 updates in one webhook;
  wasim always sends one, so loops over `entry` and `changes` are not exercised.
- **Delivery headers.** wasim sends `User-Agent: wasim-webhook` and only the
  `X-Hub-Signature-256` header. Meta's exact header set is not documented in
  the sources used, so do not depend on headers other than the signature.

### Statuses and pricing

- **Pricing is simplified.** Templates bill as `regular` with their category,
  everything else is `free_customer_service`. `free_entry_point`,
  `free_group_customer_service` and the pricing semantics Meta changed on
  2026-10-01 are not modelled. Treat `pricing` as a shape check, never as a
  billing reference.
- **Timing is synthetic.** Statuses follow a fixed delay
  (`WASIM_STATUS_DELAY`). Real latency is undocumented and varies, and a real
  `read` can arrive without a `delivered`.

### Webhook delivery

- **Retries are compressed.** Meta retries for 7 days with an unpublished
  schedule. wasim retries `WASIM_WEBHOOK_ATTEMPTS` times (8 by default), from
  1 second up to 5 minutes, so a test run finishes.
- **The handshake runs only when asked.** Meta verifies whenever the callback
  URL or token is edited and refuses to deliver to an unverified endpoint.
  wasim delivers whether or not the handshake ever passed.

### Templates

- **Only 7 listing fields.** `id`, `name`, `language`, `status`, `category`,
  `components` and `parameter_format`. Meta also returns `quality_score`,
  `rejected_reason`, `sub_category` and others; asking wasim for them fails the
  whole listing with #100, where Meta would answer.
- **No approval.** A seeded template is approved unless the seed says
  otherwise, and its status never changes on its own.
- **`limit` is capped at 100.** Meta documents only a minimum; the cap is a
  production consumer's observation, not a documented fact.

### Requests

- **Unknown fields are ignored.** Meta may reject misspelled or unsupported
  parameters with #100. wasim reads only the fields it emulates, so a typo in
  an optional field passes locally.
- **Rate limits are not enforced.** Pair rate (131056), throughput (130429) and
  messaging limits only happen when injected as failure rules.
- **Media never expires.** Meta drops media ids after 7 days (received) or 30
  days (uploaded). wasim keeps them until a reset. The 5 minute URL expiry is
  emulated.
- **Error wording is wasim's own.** Codes, HTTP statuses, `type` and the
  envelope shape follow Meta; `error_data.details` names the offending field in
  wasim's words. Match on `code`, never on text.

## Not verified against real traffic

Some emulated behaviour rests on secondary sources: an archived Meta page or
what production consumers report. The research notes keep the full list under
"Open questions". The ones that most affect consumers:

| Question | wasim's answer |
|---|---|
| Does 131047 arrive by HTTP or by webhook? | Webhook by default, configurable |
| Is 130429 an HTTP 400 or 429? | 400 |
| Is `recipient_type` required? | No |
| Does `message_status` appear on non-template sends? | No |
| Are WhatsApp webhook bodies `\u` escaped? | Yes by default, configurable |
| Does a quoted plain message carry `context`? | Yes, `{from, id}` |
| Unknown object id: HTTP 404 code 803, or HTTP 400 code 100 subcode 33? | 400, code 100, subcode 33 |

A short capture session against a real test number would settle most of them.
If you have one, please open an issue with the raw request and response bytes,
personal data removed.
