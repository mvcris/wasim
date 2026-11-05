# WhatsApp Cloud API basics for a local emulator

Fetch date for every source: 2026-11-05. Scope: what wasim must implement for a solid basic version.

**Status per claim**

| Status | Meaning |
|---|---|
| CONFIRMED | Read directly from a Meta primary source (docs page, `meta/openapi`, `fbsamples`) |
| SECONDARY | Fixture, mirror, archived copy, or behaviour observed by a consumer (a production integration or whaloc). Not stated by a live Meta page |
| UNVERIFIED | Not confirmed by anything read. Possibly from memory. Treat as a hypothesis |

**How the sources were read.** `developers.facebook.com/documentation/business-messaging/whatsapp/...` serves raw Markdown when fetched with a plain User-Agent (append `.md`), and `llms.txt` indexes all 368 pages. A browser-like User-Agent got HTTP 400, and WebFetch paraphrases, so neither was relied on for shapes. 27 pages of the index returned HTML instead of Markdown and were not read; the relevant ones are `webhooks/reference/pricing`, `webhooks/message_echoes` and `messages/message-with-link`. Graph API guide pages under `/docs/graph-api/` were read as text extracted from HTML. Meta now calls the WABA a "Messaging account" in prose; the wire value stays `whatsapp_business_account`.

## Findings that change the design

1. **Consumers need a base-URL setting.** A client that hardcodes `https://graph.facebook.com/` and makes only the version configurable cannot be pointed at an emulator; it needs a base-URL setting first. Absolute URLs in responses (`paging.next`, the media `url`) are then followed verbatim, so wasim must mint them against its own public address.
2. **Errors have two channels and the docs do not say which code uses which.** The live error page dropped the HTTP status column and states only "synchronously, asynchronously, or sometimes both" ([S28]). A production integration observes 131047 as HTTP 200 followed by a `failed` webhook, while an archived Meta table lists it as HTTP 400 ([A1]). Every simulated failure needs a configurable channel, not a fixed one.
3. **Only exactly HTTP 200 acknowledges a webhook.** "an HTTP status code other than 200" is a failure to retry ([S9], [S8]); read literally, 201 and 204 fail too. Duplicates, retries and concurrent delivery are documented behaviour, so they are features to emulate, not edge cases.
4. **Webhook dispatch must not block the Graph API.** Consumers commonly await the media download inside their webhook handler, so they call back into wasim during an in-flight POST. Delivery has to run independently of request handling.
5. **Signature input is the bytes on the wire, and those bytes likely use `\uXXXX` escapes.** Meta documents this for Messenger ([G7]) but not for WhatsApp. Serialize once, then sign and send that same buffer. See section 3.
6. **Payload shape depends on version and on rollout state, not just on the type.** The `conversation` object disappears from v24.0 on ([S16]), and BSUID fields (`user_id`, `from_user_id`, `recipient_user_id`) have been in webhooks since April 2026 but appear only on a separate page, not in the per-type references ([S32]).
7. **The docs mark `recipient_type` as required; a production consumer omits it and succeeds.** ([S7], [O1] vs observed production traffic.) Do not validate strictly from the spec without a compatibility mode.

## 1. Send: `POST /{version}/{phone-number-id}/messages`

### Common envelope

CONFIRMED ([S1], [S7], [O1]).

| Field | Rule | Status |
|---|---|---|
| `messaging_product` | Required, `"whatsapp"` | CONFIRMED [S7] |
| `recipient_type` | `individual` or `group`. Marked required in the reference and in [O1], yet a production consumer never sends it | CONFIRMED as documented; SECONDARY that omission is accepted (observed in a production integration) |
| `to` | User phone number. Plus signs, hyphens, parentheses and spaces are supported | CONFIRMED [S1] |
| `recipient` | BSUID or parent BSUID. If both are present, `to` wins | CONFIRMED [S32] |
| `type` | The message type; a property of the same name carries the content | CONFIRMED [S1] |
| `context.message_id` | wamid being replied to | CONFIRMED [S6] |
| `messaging_account_id` | Only when the token reaches more than one Messaging account on the number | CONFIRMED [S1] |

Without a leading `+`, the business number's country code is prepended, so `(631) 555-1234` from an Indian number goes to `+916315551234` ([S1], CONFIRMED). For Brazil and Mexico "the extra added prefix of the phone number may be modified by the Cloud API" ([S1], CONFIRMED), which is why `contacts[].input` and `contacts[].wa_id` can differ. A production consumer depends on this distinction for the Brazilian mobile ninth digit.

### Text

CONFIRMED ([S2]). `preview_url` is optional. `body` max 4096.

```json
{
  "messaging_product": "whatsapp",
  "recipient_type": "individual",
  "to": "+16505551234",
  "type": "text",
  "text": {
    "preview_url": true,
    "body": "As requested, here's the link to our latest product: https://www.meta.com/quest/quest-3/"
  }
}
```

What a minimal production consumer sends: `{"messaging_product":"whatsapp","to":"...","type":"text","text":{"body":"..."}}`.

### Interactive reply buttons

CONFIRMED ([S3]). `header` and `footer` are optional. A minimal consumer sends only `body` and `action`.

```json
{
  "messaging_product": "whatsapp",
  "recipient_type": "individual",
  "to": "+16505551234",
  "type": "interactive",
  "interactive": {
    "type": "button",
    "header": { "type": "text", "text": "Workshop Details" },
    "body": { "text": "Use the buttons if you need to reschedule." },
    "footer": { "text": "Lucky Shrub" },
    "action": {
      "buttons": [
        { "type": "reply", "reply": { "id": "change-button", "title": "Change" } },
        { "type": "reply", "reply": { "id": "cancel-button", "title": "Cancel" } }
      ]
    }
  }
}
```

Header types: `document`, `image`, `text`, `video`. Image form: `{"type":"image","image":{"id":"2762702990552401"}}` or `{"type":"image","image":{"link":"https://..."}}` ([S3], CONFIRMED).

### Interactive list

CONFIRMED ([S4]). A production consumer sends one section with no `title`, no header, no footer, and `description` only when set. The reference says section `title` is "Required if the message has more than one section" ([S7]) while the guide marks it plainly required ([S4]).

```json
{
  "messaging_product": "whatsapp",
  "recipient_type": "individual",
  "to": "+16505551234",
  "type": "interactive",
  "interactive": {
    "type": "list",
    "header": { "type": "text", "text": "Choose Shipping Option" },
    "body": { "text": "Which shipping option do you prefer?" },
    "footer": { "text": "Lucky Shrub" },
    "action": {
      "button": "Shipping Options",
      "sections": [
        {
          "title": "I want it ASAP!",
          "rows": [
            { "id": "priority_express", "title": "Priority Mail Express", "description": "Next Day to 2 Days" },
            { "id": "priority_mail", "title": "Priority Mail" }
          ]
        }
      ]
    }
  }
}
```

### Template, positional parameters

CONFIRMED ([S5]). Values must appear in placeholder order.

```json
{
  "messaging_product": "whatsapp",
  "recipient_type": "individual",
  "to": "+16505551234",
  "type": "template",
  "template": {
    "name": "order_confirmation",
    "language": { "code": "en_US" },
    "components": [
      {
        "type": "body",
        "parameters": [
          { "type": "text", "text": "Jessica" },
          { "type": "text", "text": "SKBUP2-4CPIG9" }
        ]
      }
    ]
  }
}
```

### Template, named parameters

CONFIRMED ([S5]). Values may appear in any order. Names are lowercase characters and underscores.

```json
{
  "messaging_product": "whatsapp",
  "recipient_type": "individual",
  "to": "+16505551234",
  "type": "template",
  "template": {
    "name": "order_confirmation",
    "language": { "code": "en_US" },
    "components": [
      {
        "type": "body",
        "parameters": [
          { "type": "text", "parameter_name": "first_name", "text": "Jessica" },
          { "type": "text", "parameter_name": "order_number", "text": "SKBUP2-4CPIG9" }
        ]
      }
    ]
  }
}
```

Fixed-body templates: a production consumer sends `"components": []`. No doc page shows an empty array being accepted: SECONDARY. wasim must accept it.

Quick-reply payload override at send time, so a tap's `button.payload` carries a chosen value ([S36], CONFIRMED on the carousel template page; generic availability UNVERIFIED).

```json
{ "type": "button", "sub_type": "quick_reply", "index": 0, "parameters": [ { "type": "payload", "payload": "more-aloes" } ] }
```

### Reply with `context`

CONFIRMED ([S6]). Not valid on reaction messages. The bubble is not displayed when replying with a template, or when the quoted message was deleted or moved to long term storage (typically after 30 days).

```json
{
  "messaging_product": "whatsapp",
  "recipient_type": "individual",
  "to": "+16505551234",
  "context": { "message_id": "wamid.HBgLMTY0NjcwNDM1OTUVAgASGBQzQTdCNTg5RjY1MEMyRjlGMjRGNgA=" },
  "type": "text",
  "text": { "body": "You're welcome, Pablo!" }
}
```

### Success response

CONFIRMED ([S1], [S2]). HTTP 200 means accepted, not delivered ([S1]). Consumers typically read only `messages[0].id` and treat its absence as a failure.

```json
{
  "messaging_product": "whatsapp",
  "contacts": [ { "input": "+16505551234", "wa_id": "16505551234" } ],
  "messages": [ { "id": "wamid.HBgLMTY0NjcwNDM1OTUVAgARGBI1RjQyNUE3NEYxMzAzMzQ5MkEA" } ]
}
```

| Variation | Rule | Status |
|---|---|---|
| `messages[].message_status` | Docs: only for a template being paced ([S1]). Enum `accepted`, `held_for_quality_assessment`, `paused` ([S7], [O1]). The docs' sample value in the [S1] table is a wamid, which looks like a doc error | CONFIRMED as documented |
| `message_status: "accepted"` on a normal send | Present in a captured production response ([W1] `docs/SPEC.md`, section "Response samples") | SECONDARY |
| Sent to `recipient` (BSUID) | `contacts[0]` has `input` and `user_id`; `wa_id` omitted ([S32]) | CONFIRMED |
| Group send | `messages[].group_id` added ([S1]) | CONFIRMED |

### Other shapes on the same endpoint

Mark as read returns `{"success": true}` and has no `to` or `type` ([S40], CONFIRMED). It also marks earlier messages in the conversation as read; an invalid id yields 131009 ([S40], CONFIRMED). Typing indicator adds `"typing_indicator": {"type": "text"}` to the same body ([S41], CONFIRMED).

```json
{ "messaging_product": "whatsapp", "status": "read", "message_id": "<WAMID>" }
```

### Auth behaviour

| Situation | Behaviour | Status |
|---|---|---|
| Header | `Authorization: Bearer <token>`, required on all endpoints | CONFIRMED [S7] |
| `Content-Type` | Required; one of `application/json`, `application/x-www-form-urlencoded`, `multipart/form-data` | CONFIRMED [S7] |
| Invalid or expired token | HTTP 401, `{"error":{"message":"Invalid OAuth access token","type":"OAuthException","code":190,"fbtrace_id":"..."}}` on template endpoints | CONFIRMED [S25]; SECONDARY for `/messages` ([A1] lists 190 as 401) |
| No token | Varies by endpoint: code 200 ("Provide valid app ID") on certain GETs, "Other endpoints may return error code 190 or 104" | CONFIRMED [S28] |
| Missing permission | Codes 3, 10, 200 to 299; HTTP 403 example with code 200 on template listing | CONFIRMED [S28], [S25] |
| Token content | Real tokens are opaque; wasim should accept configured values and offer an accept-any mode | Design choice |

## 2. Webhooks

### `GET` verification handshake

CONFIRMED ([S8]).

```
GET <CALLBACK_URL>?hub.mode=subscribe&hub.challenge=1158201444&hub.verify_token=vibecoding
```

- Sent whenever the callback URL or verify token is set or edited ([S8]).
- Valid: respond HTTP 200 with the `hub.challenge` value. Invalid: a 400-level status or anything other than 200 ([S8]).
- Anything else leaves the endpoint unverified and no webhooks are sent ([S8]).
- `hub.mode` is always `subscribe` ([G5]). `hub.challenge` is "a random string" ([S8]) or "an int" ([G5]); send digits to satisfy both.
- A typical consumer echoes the challenge, returns 403 on a token mismatch, and also checks `hub.mode`.

### `POST` envelope

CONFIRMED ([S8], [S10]). Headers: `Content-Type: application/json`, `X-Hub-Signature-256: sha256=<hash>`, `Content-Length`. Payload up to 3 MB ([S9]).

```json
{
  "object": "whatsapp_business_account",
  "entry": [
    {
      "id": "102290129340398",
      "changes": [
        {
          "value": {
            "messaging_product": "whatsapp",
            "metadata": { "display_phone_number": "15550783881", "phone_number_id": "106540352242922" },
            "contacts": [ { "profile": { "name": "Sheena Nelson" }, "wa_id": "16505551234" } ],
            "messages": [
              {
                "from": "16505551234",
                "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTRBNjU5OUFFRTAzODEwMTQ0RgA=",
                "timestamp": "1749416383",
                "type": "text",
                "text": { "body": "Does it come in another color?" }
              }
            ]
          },
          "field": "messages"
        }
      ]
    }
  ]
}
```

| Path | Rule | Status |
|---|---|---|
| `object` | Always `whatsapp_business_account` | CONFIRMED [O1] `WebhookPayload` |
| `entry[].id` | Messaging account (WABA) id | CONFIRMED [S10] |
| `entry[].time` | Present in the generic Graph webhook, absent from every WhatsApp sample | CONFIRMED [G5] vs [S10]; wasim should omit it |
| `changes[].field` | `messages` for inbound, statuses and errors | CONFIRMED [S10] |
| `value.metadata` | `display_phone_number` in digits, `phone_number_id` | CONFIRMED [S10] |
| `value.contacts` | Inbound messages. Consumers commonly take `profile.name` as the user's display name, keyed by `wa_id` | CONFIRMED [S10] |
| `value.messages` | Inbound; `value.statuses` for outgoing message status; `value.errors` for system, app or account errors | CONFIRMED [S10] |
| `messages[].timestamp` | String of Unix seconds in every example. The reference defines it as "when the webhook was triggered", while a production consumer reads it as the send time and anchors the 24h window on it | CONFIRMED [S11] |
| Mixed `messages` and `statuses` in one `value` | Docs describe them as distinct structures; a production consumer handles both in one delivery | UNVERIFIED that Meta ever mixes them |

Other fields ride on inbound messages too: `referral` for Click to WhatsApp ads, and `context.forwarded` or `context.frequently_forwarded` for forwards ([S11], CONFIRMED). The `messages` field has a sibling set of fields such as `message_template_status_update` ([S9], CONFIRMED). A production consumer reads only `messages` content and ignores `field`.

### Inbound message objects

All below are `value.messages[0]`, and each has the envelope above with its `contacts` block.

**Text.** CONFIRMED ([S11]).

```json
{
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTRBNjU5OUFFRTAzODEwMTQ0RgA=",
  "timestamp": "1749416383",
  "type": "text",
  "text": { "body": "Does it come in another color?" }
}
```

**Image.** CONFIRMED ([S12]). `url` is in the current reference; the [O1] v23 example lacks it, and a production consumer ignores it and resolves through `GET /{media-id}`.

```json
{
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTRBNjU5OUFFRTAzODEwMTQ0RgA=",
  "timestamp": "1744344496",
  "type": "image",
  "image": {
    "caption": "Taj Mahal",
    "mime_type": "image/jpeg",
    "sha256": "SfInY0gGKTsJlUWbwxC1k+FAD0FZHvzwfpvO0zX0GUI=",
    "id": "1003383421387256",
    "url": "https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=133..."
  }
}
```

**Document.** CONFIRMED ([S13]). A consumer may use `caption`, then `filename`, as the message body.

```json
{
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTRBNjU5OUFFRTAzODEwMTQ0RgA=",
  "timestamp": "1744344496",
  "type": "document",
  "document": {
    "caption": "my receipt",
    "filename": "receipt.pdf",
    "mime_type": "application/pdf",
    "sha256": "V5OPpLD/gEG6Xjg0MbmQDLFgcKsL+j5LfY4ny/pZ4MY=",
    "id": "622684793477189",
    "url": "https://lookaside.fbsbx.com/whatsapp_business/attachments/?mid=133..."
  }
}
```

**Interactive `button_reply`.** CONFIRMED ([S14]). `context.id` is the wamid of the message holding the button. `context.from` is the business display number.

```json
{
  "context": { "from": "15550783881", "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgARGBI3MEM2RUJFNkI0RENGQTVDRjUA" },
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQTZBQzg0MzQ4QjRCM0NGNkVGOAA=",
  "timestamp": "1750025136",
  "type": "interactive",
  "interactive": {
    "type": "button_reply",
    "button_reply": { "id": "cancel-button", "title": "Cancel" }
  }
}
```

**Interactive `list_reply`.** CONFIRMED ([S14]). `description` is echoed.

```json
{
  "context": { "from": "15550783881", "id": "wamid.HBgLMTQxMjU1NTA4MjkVAgASGBQzQUNCNjk5RDUwNUZGMUZEM0VBRAA=" },
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQUFERjg0NDEzNDdFODU3MUMxMAA=",
  "timestamp": "1749854575",
  "type": "interactive",
  "interactive": {
    "type": "list_reply",
    "list_reply": { "id": "priority_express", "title": "Priority Mail Express", "description": "Next Day to 2 Days" }
  }
}
```

**Template quick-reply `button`.** CONFIRMED ([S15]). The reference shows `payload` equal to the label unless overridden at send time ([S36]). A production consumer's code describes `payload` as "the developer-defined id", but without an override at send time, expect the label.

```json
{
  "context": { "from": "15550783881", "id": "wamid.HBgLMTQxMjU1NTA4MjkVAgASGBQzQUNCNjk5RDUwNUZGMUZEM0VBRAA=" },
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQUFERjg0NDEzNDdFODU3MUMxMAA=",
  "timestamp": "1750091045",
  "type": "button",
  "button": { "payload": "Unsubscribe", "text": "Unsubscribe" }
}
```

**Quoted reply via `context`.** Status: SECONDARY. No live Meta page documents a user swiping to quote a plain message. The reference lists `context` only for the "Message business" button, for forwards, and for interactive or button taps ([S11], [S14], [S15]); [O1] `ContextObject` is even described as "Only included if message via a 'Message business' button". The shape below is inferred from those `context` objects. A production consumer reads `context.id` regardless of type, and whaloc models the same `{from, id}` quote, merged with forward flags ([W1] `docs/SPEC.md`, "Notable fixture facts").

```json
{
  "context": { "from": "15550783881", "id": "wamid.<QUOTED_MESSAGE_ID>" },
  "from": "16505551234",
  "id": "wamid.<THIS_MESSAGE_ID>",
  "timestamp": "1749416383",
  "type": "text",
  "text": { "body": "Yes, that one" }
}
```

**Unsupported.** CONFIRMED ([S18]). Consumers typically turn it into a placeholder. [O1] omits the `unsupported` key in its example.

```json
{
  "from": "16505551234",
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQUFERjg0NDEzNDdFODU3MUMxMAA=",
  "timestamp": "1750090702",
  "errors": [
    {
      "code": 131051,
      "title": "Message type unknown",
      "message": "Message type unknown",
      "error_data": { "details": "Message type is currently not supported." }
    }
  ],
  "type": "unsupported",
  "unsupported": { "type": "edit" }
}
```

### Status objects

All are `value.statuses[0]`. The `value` has `metadata` and no `contacts` in the per-type reference ([S16], CONFIRMED), but see the BSUID note below.

**`sent`**, with `conversation` and `pricing`. CONFIRMED ([S16]).

```json
{
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQUFERjg0NDEzNDdFODU3MUMxMAA=",
  "status": "sent",
  "timestamp": "1750030073",
  "recipient_id": "16505551234",
  "conversation": {
    "id": "72b14d6bd5407799e66f64d1b338e567",
    "expiration_timestamp": "1750116480",
    "origin": { "type": "marketing" }
  },
  "pricing": { "billable": true, "pricing_model": "PMP", "type": "regular", "category": "marketing" }
}
```

**`sent`, v24.0 webhook.** CONFIRMED ([S16]). Both objects omitted here: `conversation` because of the version, `pricing` because it rode on the `delivered` webhook instead.

```json
{
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgASGBQzQUFERjg0NDEzNDdFODU3MUMxMAA=",
  "status": "sent",
  "timestamp": "1750030073",
  "recipient_id": "16505551234"
}
```

**`delivered`.** CONFIRMED ([S10]). The sample is older, with `pricing_model: "CBP"`, which only applies to webhooks sent before July 1, 2025 ([S16]); use `PMP`.

```json
{
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgARGBI3MTE5MjVBOTE3MDk5QUVFM0YA",
  "status": "delivered",
  "timestamp": "1750263773",
  "recipient_id": "16505551234",
  "conversation": { "id": "6ceb9d929c9bdc4f90e967a32f8639b4", "origin": { "type": "service" } },
  "pricing": { "billable": true, "pricing_model": "CBP", "category": "service" }
}
```

**`read`.** Syntax CONFIRMED ([S16]); no live page has a full `read` sample, so this is derived from the syntax block.

```json
{
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgARGBI3MTE5MjVBOTE3MDk5QUVFM0YA",
  "status": "read",
  "timestamp": "1750263800",
  "recipient_id": "16505551234"
}
```

**`failed`.** CONFIRMED ([S16]). Consumers typically log `code`, `title` and `error_data.details`.

```json
{
  "id": "wamid.HBgLMTY1MDM4Nzk0MzkVAgARGBI0QUQ2MjA4NEYyRkExNjMyREUA",
  "status": "failed",
  "timestamp": "1751142888",
  "recipient_id": "16505551234",
  "errors": [
    {
      "code": 131049,
      "title": "This message was not delivered to maintain healthy ecosystem engagement.",
      "message": "This message was not delivered to maintain healthy ecosystem engagement.",
      "error_data": { "details": "In order to maintain a healthy ecosystem engagement, the message failed to be delivered." },
      "href": "/documentation/business-messaging/whatsapp/support/error-codes"
    }
  ]
}
```

| Rule | Status |
|---|---|
| `pricing`: only with `sent` and one of `delivered` or `read`. The object "can only appear in one or the other" | CONFIRMED [S16] |
| `conversation`: same rule, and "omitted entirely for v24.0+ unless webhook is for a free entry point conversation". `expiration_timestamp` only with `sent` | CONFIRMED [S16] |
| v23.0 and lower: `conversation.id` is unique per message | CONFIRMED [S16] |
| `pricing_model`: `PMP` now; `CBP` only for webhooks before 2025-07-01 | CONFIRMED [S16] |
| `pricing.billable` to be deprecated in a future version | CONFIRMED [S16] |
| `pricing.type`: `regular`, `free_customer_service`, `free_entry_point`, `free_group_customer_service`. Semantics changed 2026-10-01 | CONFIRMED [S16] |
| Up to three webhooks per message (sent, delivered, read) | CONFIRMED [S10] |
| A `read` can arrive with no `delivered` (user was in the chat screen) | CONFIRMED [S16] |
| `played` is a further status for voice messages | CONFIRMED [S16] |
| One message can emit both `delivered` and `failed` when a user has several devices | CONFIRMED [S30] |
| `failed` may arrive after "a minor delay" | CONFIRMED [S1] |

**`value.errors`** (system, app or account level). CONFIRMED ([S17]). Here the message has no `statuses` array; there is no wamid to correlate. Many consumers ignore this path.

```json
{
  "messaging_product": "whatsapp",
  "metadata": { "display_phone_number": "15550783881", "phone_number_id": "106540352242922" },
  "errors": [
    {
      "code": 130429,
      "title": "Rate limit hit",
      "message": "Rate limit hit",
      "error_data": { "details": "Message failed to send because there were too many messages sent from this phone number in a short period of time" },
      "href": "/documentation/business-messaging/whatsapp/support/error-codes"
    }
  ]
}
```

**BSUID additions.** CONFIRMED ([S32], which warns "subject to change"). In webhooks since April 2026; absent from the per-type reference pages.

| Location | Added |
|---|---|
| Inbound `contacts[]` | `user_id` always; `profile.username` if adopted; `parent_user_id` if enrolled |
| Inbound `messages[]` | `from_user_id`; `from_parent_user_id` if enrolled |
| Status `value` | A `contacts` block for sent, delivered and read |
| `statuses[]` | `recipient_user_id` always; `recipient_parent_user_id` if enrolled |
| Username adopted, no contact in 30 days, not in contact book | Phone number fields (`wa_id`, `from`) omitted |
| Send error | 131062 `Business-scoped User ID (BSUID) recipients are not supported for this message.` |

## 3. Signature: `X-Hub-Signature-256`

```
X-Hub-Signature-256: sha256=<lowercase hex HMAC-SHA256(key = app secret, message = request body bytes)>
```

| Claim | Status |
|---|---|
| HMAC-SHA256 of the JSON payload, app secret as the key, header value prefixed `sha256=` | CONFIRMED [S8], [G5] |
| Hex digest. Docs example `b63bb356dff0f1c24379efea2d6ef0b2e2040853339d1bcf13f9018790b1f7d2`; Meta's sample compares `hexdigest()` of the raw request bytes | CONFIRMED [S8], [O3] |
| "we generate the signature using an escaped unicode version of the payload, with lowercase hex digits" (`äöå` becomes `\u00e4\u00f6\u00e5`) | CONFIRMED for Messenger Platform [G7]; SECONDARY for WhatsApp ([W1] asserts it). No WhatsApp page mentions it |
| Whether `/` is also escaped as `\/` | UNVERIFIED |

Consequence: a consumer verifying against the raw request bytes passes either way, while one that re-serializes parsed JSON breaks on escaped non-ASCII. Emitting the escaped form by default exposes that bug locally, so it is the useful default. Serialize once and use the same buffer for HMAC and body.

Conformance vectors (computed for this document with key `test_app_secret`, not from Meta):

| Body bytes | Hex digest |
|---|---|
| `{"object":"whatsapp_business_account","entry":[]}` | `dec6a680679a968e7e0319355b23599cbfc54502c0784668e9618baa0c633756` |
| `{"text":{"body":"caf\u00e9"}}` (literal backslash-u escape) | `d36eb0f4e8ed7a1458654758c875f8906c154262156b732bbdf52856281cd3fb` |
| `{"text":{"body":"café"}}` (raw UTF-8) | `d0c411924f46299702b190bbe4b5da59c4bb5641ea114e4e11535ad85929c4f0` |

A careful consumer rejects a missing body, a missing prefix, and a signature of the wrong length. Those are useful negative scenarios for a test mode that sends a bad signature.

## 4. Delivery behaviour

| Topic | Documented behaviour | Status |
|---|---|---|
| Acknowledge | Exactly HTTP 200. "an HTTP status code other than 200" is a failure | CONFIRMED [S9], [S8] |
| Retries | "retried immediately, then a few more times with decreasing frequency over the next 7 days" | CONFIRMED [S8] |
| Retry curve | "exponential backoff". No interval or attempt count is published | CONFIRMED [S19]; schedule UNVERIFIED |
| Retry window history | Cut from 30 days to 7 days | CONFIRMED [S31] |
| Generic Graph vs WhatsApp | Graph page says 36 hours; WhatsApp pages say 7 days. Use 7 days | CONFIRMED [G5], [S9] |
| Duplicates | Retries "can result in duplicate webhook notifications"; the server "should handle deduplication" | CONFIRMED [S9], [S8] |
| Fan-out | Retries go to all apps subscribed for the account | CONFIRMED [S9] |
| Drop | "Unacknowledged responses will be dropped after 7 days". No API fetches history | CONFIRMED [S8] |
| Batching | Up to 1000 updates per POST, "batching cannot be guaranteed" | CONFIRMED [S8] |
| Concurrency | "Meta attempts to deliver webhooks concurrently" | CONFIRMED [S19] |
| Ordering of webhooks | No guarantee is stated; concurrency implies none. Only history webhooks mention order explicitly (`chunk_order`) | UNVERIFIED as an explicit statement |
| Ordering of outbound | Delivery order "is not guaranteed to match the order of your API requests" | CONFIRMED [S1] |
| Timeout | No WhatsApp number. Latency standard: median at most 250 ms, under 1% above 1 s | CONFIRMED [S19]; timeout UNVERIFIED |
| Messenger timeout | "within 5 or less seconds" (Messenger only) | CONFIRMED [G7], not WhatsApp |
| Message TTL | Undeliverable messages retried 30 days (authentication templates 10 minutes), then dropped | CONFIRMED [S1] |
| Capacity guidance | Plan for 3x outgoing plus 1x incoming traffic in webhooks | CONFIRMED [S19] |
| Latency between send and `sent` | Not documented | UNVERIFIED |

Robust consumers expect out-of-order and duplicate statuses and enforce monotonic progression, so scenarios for these have immediate test value.

## 5. Media

### `GET /{version}/{media-id}`

CONFIRMED ([S21], [S22]). Bearer token required. Optional `?phone_number_id=` restricts to media uploaded on that number.

```json
{
  "messaging_product": "whatsapp",
  "url": "<MEDIA_URL>",
  "mime_type": "<MEDIA_MIME_TYPE>",
  "sha256": "<SHA_256_HASH>",
  "file_size": "303833",
  "id": "2621233374848975"
}
```

| Claim | Status |
|---|---|
| `file_size` is a string in the reference and in the example | CONFIRMED [S22], [O1] |
| `sha256` is base64 in webhooks (44 chars ending `=`) | CONFIRMED by example [S12], [S13] |
| `sha256` encoding on this endpoint | Not stated (`<HASH>` in [O1]). SECONDARY that it is base64 ([W1]) |
| `phone_number_id` mismatch error shape | UNVERIFIED; docs say only that the request is not processed |

### Downloading the URL

| Claim | Status |
|---|---|
| Fetch the returned absolute URL with `Authorization: Bearer`. "If you omit your token, the request will fail" | CONFIRMED [S21] |
| URL expires after 5 minutes; re-query the id for a new one | CONFIRMED [S21], [S23] |
| Failure: `404 Not Found`; then get a new URL, then renew the token. Reference also lists 400, 401, 500 | CONFIRMED [S21], [S23] |
| Response has a `Content-Type` naming the MIME type ([S21]); the reference says `application/octet-stream` ([S23]) | CONFIRMED, conflicting. Send the real MIME type |
| Host in samples: `lookaside.fbsbx.com/whatsapp_business/attachments/?mid=...` | CONFIRMED [S12] |
| Media IDs in webhooks expire after 7 days; uploaded IDs and files after 30 days | CONFIRMED [S21], [S31] |
| A production consumer sends the bearer token on both hops, prefers lookup `mime_type` over the `Content-Type` header, rejects on `Content-Length` over its cap, and re-checks real size | Observed in a production integration |
| Range requests, redirects | Not documented. SECONDARY: whaloc supports Range and forbids redirects for its consumer ([W1]) |

### `POST /{version}/{phone-number-id}/media`

CONFIRMED ([S21], [S24]). `multipart/form-data`. Response `{"id":"<MEDIA_ID>"}`. Example: `{"id":"1037543291543636"}`.

```
curl 'https://graph.facebook.com/<API_VERSION>/<PHONE_NUMBER_ID>/media' \
  -H 'Authorization: Bearer <ACCESS_TOKEN>' \
  -F 'messaging_product=whatsapp' \
  -F 'file=@<FILE_PATH_AND_NAME>;type=<MIME_TYPE>'
```

Conflict: the guide lists a `type` form field as required, while its own curl sample, the reference, and [O1] all omit it. wasim should accept both and take the MIME from `type` or the file part. `DELETE /{media-id}` returns `{"success":true}` ([S21], CONFIRMED).

## 6. Templates: `GET /{version}/{waba-id}/message_templates`

CONFIRMED ([S25], [S26]). Bearer token. The token needs the `whatsapp_business_management` permission.

| Parameter | Rule | Status |
|---|---|---|
| `fields` | Comma list: `id`, `category`, `components`, `language`, `name`, `parameter_format`, `quality_score`, `rejected_reason`, `status`, `sub_category`, `previous_category`, `last_updated_time`, `message_send_ttl_seconds`, others | CONFIRMED [S25] |
| `limit` | min 1. Max not documented. A production consumer's code calls 100 "Meta's page cap" | CONFIRMED min; max SECONDARY |
| `after`, `before` | Cursors | CONFIRMED [S25] |
| `name`, `status` | Filters; `status=approved` appears in examples | CONFIRMED [O1], [S26] |
| Unknown or too-new field | Fails the whole listing with #100, per a production consumer's code comment | SECONDARY |
| Invalid cursor, v24.0+ | Error 131059; retry without cursors | CONFIRMED [S31] |

A typical production call: `fields=name,language,status,category,components&limit=100`.

### Response

Structure CONFIRMED ([S26], [S25]). Assembled from the [O1] example plus the `paging.next` of [S26].

```json
{
  "data": [
    {
      "name": "2023_april_promo",
      "components": [
        { "type": "HEADER", "format": "TEXT", "text": "Fall Sale" },
        {
          "type": "BODY",
          "text": "Hi {{1}}, our Fall Sale is on! Use promo code {{2}} Get an extra 25% off every order above $350!",
          "example": { "body_text": [ [ "Mark", "FALL25" ] ] }
        },
        { "type": "FOOTER", "text": "Not interested in any of our sales? Tap Stop Promotions" },
        { "type": "BUTTONS", "buttons": [ { "type": "QUICK_REPLY", "text": "Stop promotions" } ] }
      ],
      "language": "en_US",
      "status": "APPROVED",
      "category": "MARKETING",
      "id": "920070352646140"
    }
  ],
  "paging": {
    "cursors": { "before": "MAZDZD", "after": "MjQZD" },
    "next": "https://graph.facebook.com/v23.0/10229..."
  }
}
```

| Claim | Status |
|---|---|
| `next` absent means last page. Pages may be empty and still have `next`. Stop when `next` disappears, not on short pages | CONFIRMED [G6] |
| `next` and `previous` are absolute URLs. Consumers follow `next` verbatim | CONFIRMED [S25], [G6] |
| Do not store cursors; deleted items invalidate them | CONFIRMED [G6] |
| `parameter_format` response enum is uppercase: `NAMED`, `POSITIONAL`. Creation uses lowercase `named`, `positional`, defaulting to positional | CONFIRMED [S25], [S5] |
| Which version introduced `parameter_format` | UNVERIFIED |
| Response `example` for named params | Creation uses `body_text_named_params` ([S27], CONFIRMED). The reference's `Example` lists only `header_text`, `header_handle`, `body_text`, so the response form is UNVERIFIED |
| `status` enum: `APPROVED`, `ARCHIVED`, `DELETED`, `DISABLED`, `IN_APPEAL`, `LIMIT_EXCEEDED`, `PAUSED`, `PENDING`, `PENDING_DELETION`, `REJECTED` | CONFIRMED [S25] |
| `category` enum: `AUTHENTICATION`, `FREE_SERVICE`, `MARKETING`, `UTILITY` | CONFIRMED [S25] |
| Component `type` enum: `BODY`, `BUTTONS`, `CAROUSEL`, `FOOTER`, `HEADER`, `LIMITED_TIME_OFFER`. `format`: `DOCUMENT`, `IMAGE`, `LOCATION`, `TEXT`, `VIDEO` | CONFIRMED [S25] |
| Button `type` enum includes `QUICK_REPLY`, `URL`, `PHONE_NUMBER`, `COPY_CODE`, `FLOW`, `OTP`, `CATALOG`, `MPM`; [S27] also shows `VOICE_CALL` | CONFIRMED [S25], [S27] |
| Only `APPROVED` can be sent | CONFIRMED [S5] |
| Names unique per language, not globally | CONFIRMED [S5] |
| Component `type` case | Uppercase in responses; [S5] and [S27] mix cases on creation. Accept both on input. Consumers read `BODY` in uppercase |

## 7. Errors

### Synchronous envelope

CONFIRMED ([S28], [G4]).

```json
{
  "error": {
    "message": "(#130429) Rate limit hit",
    "type": "OAuthException",
    "code": 130429,
    "error_data": {
      "messaging_product": "whatsapp",
      "details": "Cloud API message throughput has been reached."
    },
    "error_subcode": 2494055,
    "fbtrace_id": "Az8or2yhqkZfEZ-_4Qn_Bam"
  }
}
```

| Field | Rule | Status |
|---|---|---|
| `message` | Code plus title, `(#130429) Rate limit hit` | CONFIRMED [S28] |
| `type` | Usually `OAuthException`; `GraphMethodException` also appears | CONFIRMED [S28], [S25] |
| `code` | Integer. Build on this and `details` | CONFIRMED [S28] |
| `error_data.details` | Most useful field; present on most, not all | CONFIRMED [S28] |
| `error_subcode` | "Deprecated. Will not be returned in v16.0+ responses", yet the same page's example includes one, and BSUID errors still use one | CONFIRMED, internally contradictory [S28], [S32] |
| `fbtrace_id` | Trace id | CONFIRMED [S28] |
| `is_transient`, `error_user_title`, `error_user_msg` | Optional, generic Graph | CONFIRMED [S25], [G4] |
| Webhook error object | `code`, `title`, `message`, `error_data.details`, `href`; different from the sync envelope | CONFIRMED [S16], [S17] |

### Codes for v1

"Archive HTTP" is from a November 2025 copy of Meta's error page, which then had a status column ([A1], SECONDARY). The live page no longer has it, and states only that errors arrive synchronously, asynchronously, "or sometimes through both" ([S28], CONFIRMED). Meanings are CONFIRMED from [S28] unless noted.

| Code | Documented meaning | Archive HTTP | Channel evidence | Channel status | wasim default |
|---|---|---|---|---|---|
| 131047 | More than 24 hours since the recipient last replied | 400 | A production consumer: "only ever arrives on the statuses webhook". Archive implies HTTP also possible | SECONDARY | HTTP 200, then `failed` webhook; toggle to sync |
| 131026 | Unable to deliver: not a WhatsApp number, terms not accepted, or old client version | 400 | Calling pages say "error webhook" for old clients ([S35]) | CONFIRMED async on that path; sync SECONDARY | Async |
| 131049 | Not delivered to maintain healthy ecosystem engagement | 400 | `failed` status example ([S16]) | CONFIRMED async | Async |
| 130472 | Not sent as part of an experiment. Resending gives the same error | 400 | Experiments page: `failed` status webhook ([S29]) | CONFIRMED async | Async |
| 132000 | Parameter count mismatch | 400 | A production consumer: "the send returns #132000". Captured HTTP 400 with `details` of the form `body: number of localizable_params (1) does not match the expected number of params (3)` ([W1]) | SECONDARY sync | Sync 400 |
| 132001 | Template does not exist in that language, or is not approved | 404 | Only the archive | SECONDARY sync | Sync 404 |
| 131051 | Unsupported message type | 400 | Inbound `unsupported` webhook ([S18], CONFIRMED). Outbound sync only by archive | Both | Inbound async; outbound sync 400 |
| 100 | Unsupported or misspelled parameters, or over a length restriction | 400 | HTTP 400 examples ([S25], [S28]) | CONFIRMED sync | Sync 400 |
| 190 | Access token expired | 401 | HTTP 401 example ([S25]) | CONFIRMED sync | Sync 401 |
| 131030 | Recipient number not in allowed list. Only with public test numbers. **Not on the WhatsApp error page**; defined on the calling pages ([S33], [S34]), and absent from the archive too | Not listed | HTTP 400 on calling ([S33], CONFIRMED). For `/messages` not documented | UNVERIFIED for `/messages` | Sync 400, opt-in mode only |
| 131056 | Too many messages from sender to the same recipient in a short period | 400 | "if a send request fails, retry after 4^X seconds" ([S20]) implies HTTP. whaloc: 400 ([W1]) | SECONDARY sync | Sync 400 |
| 130429 | Throughput reached | 400 | "the API will return error code 130429" ([S19], CONFIRMED sync). Also a `value.errors` webhook example ([S17], CONFIRMED async). whaloc uses HTTP 429 with `Retry-After` ([W1]), which conflicts with the archive | CONFIRMED both; HTTP status conflicting | Sync 400, configurable |

Also cheap and useful: 131008 missing required parameter, 131009 invalid parameter value, 131021 sender equals recipient, 131053 media upload failed, 131052 inbound media too big or undownloadable, 132012 parameter format mismatch, 130403 business blocked the user, 131050 user stopped marketing, 131048 spam rate limit, 2 temporary downtime, 131000 unknown ([S28], CONFIRMED).

Validation order is not stable: "you might receive different error codes for the same API request" ([S30], CONFIRMED). Consumers should not rely on precedence, so wasim need not mimic any.

Unknown object ids: the template reference shows HTTP 404, code 803, `GraphMethodException` ([S25], CONFIRMED). whaloc states production returns HTTP 400 with code 100 and subcode 33 ([W1], SECONDARY), which matches the example fields of [O1] `GraphAPIError_2`. Treat as open.

## 8. Validation limits

All CONFIRMED from the cited page unless noted. Characters as Meta states them; whether "characters" means code points or UTF-16 units is UNVERIFIED.

### Messages

| Item | Limit | Source |
|---|---|---|
| Text `body` | 4096 | [S2] |
| Media `caption` (image, document) | 1024 | [S38], [S39] |
| Buttons: count | Max 3 | [S3] |
| Buttons: `id` | 256 | [S3] |
| Buttons: `title` | 20, unique within the message | [S3] |
| Buttons: `body.text` | 1024 | [S3] |
| Buttons: `footer.text` | 60 | [S3] |
| Buttons: text `header` | Not stated on that page; 60 on the list and CTA URL pages | [S4] |
| List: sections | Max 10 | [S4] |
| List: rows | Max 10 **across all sections combined**, min 1 | [S4] |
| List: `action.button` | 20 | [S4] |
| List: `body.text` | 4096 | [S4] |
| List: section `title` | 24 | [S4], [S7] |
| List: row `id` | 200 | [S4] |
| List: row `title` | 24 | [S4] |
| List: row `description` | 72 | [S4] |
| List: `header` | Text only, 60 | [S4] |
| List: `footer` | 60 | [S4] |

### Templates

| Item | Limit | Source |
|---|---|---|
| `name` | 512, lowercase alphanumerics and underscores | [S5] |
| Header text | 60, 1 parameter | [S27] |
| Body text | 1024 | [S27] |
| Footer | 60 | [S27] |
| Quick-reply button text | 25 | [S27] |
| Buttons | 10 total | [S27] |
| Templates per account | 250 unverified portfolio, 6000 verified | [S5] |
| Named parameter values | Length not documented | UNVERIFIED |

### Media

All from [S21].

| Type | MIME | Max |
|---|---|---|
| Audio | `audio/aac`, `audio/amr`, `audio/mpeg`, `audio/mp4`, `audio/ogg` (OPUS, mono) | 16 MB |
| Document | `text/plain`, `application/vnd.ms-excel`, `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `application/msword`, `application/vnd.openxmlformats-officedocument.wordprocessingml.document`, `application/vnd.ms-powerpoint`, `application/vnd.openxmlformats-officedocument.presentationml.presentation`, `application/pdf` | 100 MB |
| Image | `image/jpeg`, `image/png`, static `image/webp`. 8-bit RGB or RGBA. Over 4096 px on an edge rejected | 5 MB |
| Sticker | `image/webp` | 500 KB animated, 100 KB static |
| Video | `video/3gpp`, `video/mp4` (H.264, AAC) | 16 MB |

Inbound media above 100 MB yields 131052 in the webhook ([S21]). Consumers may apply their own caps on top of these, which wasim does not need to mirror.

### 24-hour customer service window

| Rule | Status |
|---|---|
| A user message or call opens a 24-hour timer; another resets it to 24 hours | CONFIRMED [S1] |
| Open: any service message type. Closed: pre-approved templates only | CONFIRMED [S1], [S5] |
| Interactive messages are service messages, so they need the window | CONFIRMED [S1] |
| Known issue: "In rare cases" a reply within the window is impossible | CONFIRMED [S1] |
| Does a quick-reply tap reopen the window? A production consumer assumes yes, and [S1] says only "messages you" | SECONDARY |
| Failure mode outside the window | See 131047 in section 7 |

### Rate limits

| Limit | Value | Error | Status |
|---|---|---|---|
| Pair rate | 1 message per 6 s to the same user (about 10/min, 600/h) | 131056 | CONFIRMED [S20] |
| Pair burst | Up to 45 in 6 s, borrowed from future quota; a burst of 20 needs about 2 min of waiting | 131056 | CONFIRMED [S20] |
| Pair retry advice | 4^X seconds, X from 0 | | CONFIRMED [S20] |
| Throughput | 80 mps per number by default, up to 1000 by upgrade | 130429 | CONFIRMED [S19], [S20] |
| Graph request rate | 200 req/h per app per account, 5000 if active; covers `message_templates` | 80007 or 4 | CONFIRMED [S20], [S28] |
| Messaging limits | Unique users outside a window in 24h (portfolio level) | Not named on [S20]. [S28] lists 131048 and 131064 as related throttles | CONFIRMED [S20], [S28] |

A pair-limit token bucket is capacity 45, refilling 1 per 6 s. That matches the numbers but is an inference: the algorithm is not documented.

## 9. Graph API versions

| Claim | Status |
|---|---|
| Latest is **v27.0**, released 2026-11-04 (the day before this research) | CONFIRMED [G1], [G2] |
| WhatsApp doc samples are already on v27.0; some older pages still show v19.0 and v23.0 | CONFIRMED [S2], [S6], [S26] |
| The word WhatsApp does not appear in the v27.0 Graph changelog, so no WhatsApp change is announced there. WhatsApp changes are logged in [S31], whose newest entry read is 2026-05-12, which predates BSUID notes on [S32] | CONFIRMED [G2], [S31] |
| Availability: v26.0 and v27.0 TBD; v25.0 until 2028-07-29; v24.0 until 2028-02-18; v23.0 until 2027-10-08; v22.0 until 2027-05-20; v21.0 until 2027-01-21; **v20.0 until 2026-09-24** | CONFIRMED [G1] |
| Each version lives at least two years | CONFIRMED [G3] |
| A call to an expired version is "defaulted to the next oldest, usable version" | CONFIRMED [G3] |
| Unversioned call uses the version set in the app dashboard | CONFIRMED [G3] |
| An app may call older versions only if available and if it has called them before | CONFIRMED [G3] |
| Newer public OpenAPI than v23.0 | Not published: [O1] is only v23.0 despite commit notes mentioning v23, v24, v25 ([O2]). The docs references are newer than it | CONFIRMED |
| Response for a nonexistent version such as `v99.0` | UNVERIFIED |
| Which version shapes a webhook. [S16] says "v24.0 webhook" without naming the source; it may be an app-level setting | UNVERIFIED |

Consequence for consumers: a client still pinned to `v20.0` is past its availability date. Per [G3] such calls now resolve to v21.0, itself ending 2027-01-21.

Emulator consequence: accept any `/v\d+\.\d+/` prefix on a single router, echo the requested version in generated `paging.next`, and gate version-sensitive output (`conversation` from v24.0, 131059 from v24.0) on a configurable effective version.

## Recommended v1 scope

This was the scope plan for v1, written before implementation. [`docs/compatibility.md`](../compatibility.md) tracks what actually shipped.

### Must-have

| Item | Justification |
|---|---|
| `POST /{v}/{phone}/messages` for text | The most common send; every consumer needs it |
| Interactive `button` and `list` with limits from section 8 | Common in service flows; limits are documented ([S3], [S4]) |
| Template send, positional and named, validated against the catalog, including `components: []` | Templates are the only way to message outside the window ([S5]); 132000 is the common integration mistake |
| `recipient_type` optional | Production consumers omit it and succeed (section 1) |
| Success response with `messages[0].id` and `contacts` | Consumers read the wamid from it ([S1]) |
| Bearer auth, sync envelope, 190 and 100 | Documented error envelope and codes ([S28], [S25]) |
| Failure injection with per-code channel (sync or async): 131047, 131026, 131049, 130472, 132000, 132001, 131051, 131056, 130429, 100, 190, 131030 | Consumers must handle these; channel ambiguity in section 7 |
| 24h window model, default async 131047 | Documented window ([S1]); channel observed in production (section 7) |
| `GET /webhook` handshake trigger | Documented verification flow ([S8]) |
| Signed `POST` with one buffer for HMAC and body, escaped non-ASCII by default | Raw-body signature check ([S8], [O3]); escaping in section 3 |
| Inbound text, image, document, `button_reply`, `list_reply`, `button` | The inbound types a typical service integration handles ([S11] to [S15]) |
| Quoted replies via `context.id` | Consumers thread replies on it (section 2) |
| `contacts[].profile.name` and `wa_id` | Present on every inbound webhook ([S10]) |
| String Unix-seconds `timestamp`, overridable to test window expiry | Documented format ([S11]); window tests need back-dating |
| Statuses sent, delivered, read, failed with errors | Documented status webhooks ([S16]) |
| `GET /{v}/{media-id}` and authenticated byte download, minted on wasim's own host | Documented two-step media flow ([S21]) |
| Webhook delivery independent of request handling | Consumers call the Graph API back while handling a webhook (finding 4) |
| `GET /{v}/{waba}/message_templates` with `fields`, `limit`, cursors, absolute `paging.next` | Consumers sync their template catalog ([S25], [G6]) |
| Retry on non-200 with fast, configurable backoff; duplicate and reorder toggles | Documented behaviour ([S8], [S9]); consumers must deduplicate |
| Any version prefix, configurable effective version | Consumers pin different versions; reason in section 9 |
| Browser chat UI as the user | Reason: stated project goal |
| Base-URL setting in the consumer (a consumer change, not wasim) | Clients that hardcode `graph.facebook.com` cannot reach an emulator (finding 1) |

### Later

| Item | Reason to defer |
|---|---|
| `POST /{v}/{phone}/media`, `DELETE /{media-id}`, outbound media sends | Less common in service flows than text, interactive and templates |
| Mark as read, typing indicator | Optional for a consumer, though the first is cheap and likely wanted |
| Outbound `context` rendering in the UI | Few consumers quote when sending |
| Pair-rate and throughput enforcement as real limiters | Injection covers the error handling; enforcement can surprise tests |
| Batching more than one update per POST | Consumers that loop over `entry` and `changes` handle it, so a toggle is enough at first |
| `value.errors` webhooks | Many consumers ignore them |
| BSUID fields and phone-hidden scenarios | Real since April 2026, but still marked "subject to change" ([S32]); add behind a flag soon |
| Template CRUD, template status webhooks | Most consumers only list templates and manage them in the dashboard |
| Reactions, audio, video, sticker, location, contacts, system messages | Consumers commonly render these as placeholders; next in line after v1 |
| Calling, Flows, Groups, Payments, Marketing Messages API | Out of scope |
| mTLS, callback override, multi-app fan-out | Local use does not need them |

## Open questions: docs vs observed behaviour

| # | Question | Docs say | Observed or other |
|---|---|---|---|
| 1 | Does 131047 come by HTTP or webhook? | Live page silent; archive says 400 | A production consumer: webhook only |
| 2 | Does 130429 return HTTP 400 or 429? | Archive 400 | whaloc 429 with `Retry-After` |
| 3 | Is `recipient_type` required? | Required | A production consumer omits it |
| 4 | Is `message_status` on all sends? | Paced templates only | Captured `accepted` on a normal send (whaloc) |
| 5 | Is non-ASCII escaped in WhatsApp webhooks? `/` too? | Messenger only | whaloc asserts yes for non-ASCII |
| 6 | Quoted reply `context` on plain messages | Undocumented | A production consumer and whaloc rely on `{from, id}` |
| 7 | Does `button.payload` equal the label by default? | Reference shows so; override only on the carousel page | A production consumer's code says developer-defined id |
| 8 | Unknown object id | 404, code 803 | whaloc: 400, code 100, subcode 33 |
| 9 | `error_subcode` | Deprecated since v16.0 | Same page's example has one; whaloc shows 33 |
| 10 | `from` and `wa_id` format | Param tables show `+16505551234`, JSON examples show digits | A production consumer and whaloc treat them as digits |
| 11 | Are `messages` and `statuses` ever in one `value`? | Described separately | A production consumer handles both; no evidence Meta mixes them |
| 12 | Which version shapes a webhook? | Unnamed | Unknown |
| 13 | Does `limit` have a cap, and is it 100? | Only a minimum | A production consumer's code says 100 |
| 14 | Actual retry schedule and timeout | "decreasing frequency", "exponential backoff", no numbers | Unknown; a test against a real app would settle it |
| 15 | Does `components: []` work for fixed-body templates? | No example | A production consumer sends it |
| 16 | `file_size` string or number? | String | Consumers should tolerate both; real responses not checked |
| 17 | Media download `Content-Type` | Real MIME in guide, octet-stream in reference | A production consumer falls back to the header only |
| 18 | Upload `type` field | Required in the guide, absent in reference and sample | Not observed |
| 19 | Does `timestamp` stay fixed across webhook retries? | "when the webhook was triggered" | A production consumer treats it as send time; retries not observed |

Most of these need one short session against a real test number. Capture raw request and response bytes (including `\u` escapes and headers) and store them as fixtures; that one session would close 1, 2, 3, 4, 5, 6, 8, 10, 15 and 16.

## Could not verify

- Any HTTP status per error code from a live page. The column was removed; only [A1] and consumer observations remain.
- WhatsApp-specific timeout, retry intervals, attempt counts, and explicit webhook ordering statements.
- Quoted reply on a plain inbound message from a primary source.
- Webhook version selection, behaviour for nonexistent path versions, `limit` max, and `parameter_format` introduction version.
- Generic availability of the quick-reply `payload` override beyond the carousel page.
- Postman: `fbsamples/WhatsApp-Business-API-Postman-Collection` is archived since 2022 and holds only a README ([O4]). Meta's workspace at `postman.com` was reachable but not readable as data, so no Postman collection was used.
- Pages not served as Markdown: `webhooks/reference/pricing`, `webhooks/message_echoes`, `messages/message-with-link`, and 24 others, none needed for the basics.
- whaloc's mirrored specs at `docs/meta-openapi/` were not used, since the primary sources were readable. The fixtures compared matched Meta's examples in structure.

## Sources

WhatsApp docs, base `https://developers.facebook.com/documentation/business-messaging/whatsapp/`:

| Id | Path |
|---|---|
| S1 | [messages/send-messages][S1] |
| S2 | [messages/text-messages][S2] |
| S3 | [messages/interactive-reply-buttons-messages][S3] |
| S4 | [messages/interactive-list-messages][S4] |
| S5 | [templates/overview][S5] |
| S6 | [messages/contextual-replies][S6] |
| S7 | [reference/whatsapp-business-phone-number/message-api][S7] |
| S8 | [webhooks/create-webhook-endpoint][S8] |
| S9 | [webhooks/overview][S9] |
| S10 | [webhooks/reference/messages][S10] |
| S11 | [webhooks/reference/messages/text][S11] |
| S12 | [webhooks/reference/messages/image][S12] |
| S13 | [webhooks/reference/messages/document][S13] |
| S14 | [webhooks/reference/messages/interactive][S14] |
| S15 | [webhooks/reference/messages/button][S15] |
| S16 | [webhooks/reference/messages/status][S16] |
| S17 | [webhooks/reference/messages/errors][S17] |
| S18 | [webhooks/reference/messages/unsupported][S18] |
| S19 | [throughput][S19] |
| S20 | [about-the-platform][S20] |
| S21 | [business-phone-numbers/media][S21] |
| S22 | [reference/media/media-api][S22] |
| S23 | [reference/media/media-download-api][S23] |
| S24 | [reference/whatsapp-business-phone-number/media-upload-api][S24] |
| S25 | [reference/whatsapp-business-account/message-template-api][S25] |
| S26 | [templates/template-management][S26] |
| S27 | [templates/components][S27] |
| S28 | [support/error-codes][S28] |
| S29 | [support/experiments][S29] |
| S30 | [support][S30] |
| S31 | [changelog][S31] |
| S32 | [business-scoped-user-ids][S32] |
| S33 | [calling/troubleshooting][S33] |
| S34 | [calling/sandbox][S34] |
| S35 | [calling/call-button-messages-deep-links][S35] |
| S36 | [templates/marketing-templates/media-card-carousel-templates][S36] |
| S38 | [messages/image-messages][S38] |
| S39 | [messages/document-messages][S39] |
| S40 | [messages/mark-message-as-read][S40] |
| S41 | [typing-indicators][S41] |

Graph API:

| Id | URL |
|---|---|
| G1 | <https://developers.facebook.com/documentation/development/graph-api/changelog> |
| G2 | <https://developers.facebook.com/documentation/development/graph-api/changelog/version27.0> |
| G3 | <https://developers.facebook.com/docs/graph-api/guides/versioning> |
| G4 | <https://developers.facebook.com/docs/graph-api/guides/error-handling> |
| G5 | <https://developers.facebook.com/docs/graph-api/webhooks/getting-started> |
| G6 | <https://developers.facebook.com/docs/graph-api/results> |
| G7 | <https://developers.facebook.com/documentation/business-messaging/messenger-platform/webhooks> |

Meta repositories:

| Id | URL |
|---|---|
| O1 | <https://raw.githubusercontent.com/meta/openapi/main/business-messaging-api_v23.0.yaml> (blob `29e678ee`, last pushed 2026-08-11) |
| O2 | <https://github.com/meta/openapi/commits/main> |
| O3 | <https://github.com/fbsamples/whatsapp-api-examples/blob/main/signature-validation-with-webhooks-payloads/app.py> |
| O4 | <https://github.com/fbsamples/WhatsApp-Business-API-Postman-Collection> |

Secondary:

| Id | URL | Note |
|---|---|---|
| A1 | <https://web.archive.org/web/20251119121645/https://developers.facebook.com/docs/whatsapp/cloud-api/support/error-codes/> | Archived copy of a Meta page, used only for HTTP status per code |
| W1 | <https://github.com/dgadelha/whaloc/blob/main/docs/SPEC.md> | MIT. Upstream paths read: `docs/SPEC.md`, `docs/fixtures/webhooks/{text-message,image-message,status-sent,status-failed,unsupported-message}.json`. Pointers only, nothing copied into wasim's design |

[S1]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/send-messages
[S2]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/text-messages
[S3]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/interactive-reply-buttons-messages
[S4]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/interactive-list-messages
[S5]: https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/overview
[S6]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/contextual-replies
[S7]: https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/whatsapp-business-phone-number/message-api
[S8]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/create-webhook-endpoint
[S9]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/overview
[S10]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages
[S11]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/text
[S12]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/image
[S13]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/document
[S14]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/interactive
[S15]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/button
[S16]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/status
[S17]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/errors
[S18]: https://developers.facebook.com/documentation/business-messaging/whatsapp/webhooks/reference/messages/unsupported
[S19]: https://developers.facebook.com/documentation/business-messaging/whatsapp/throughput
[S20]: https://developers.facebook.com/documentation/business-messaging/whatsapp/about-the-platform
[S21]: https://developers.facebook.com/documentation/business-messaging/whatsapp/business-phone-numbers/media
[S22]: https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/media/media-api
[S23]: https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/media/media-download-api
[S24]: https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/whatsapp-business-phone-number/media-upload-api
[S25]: https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/whatsapp-business-account/message-template-api
[S26]: https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/template-management
[S27]: https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/components
[S28]: https://developers.facebook.com/documentation/business-messaging/whatsapp/support/error-codes
[S29]: https://developers.facebook.com/documentation/business-messaging/whatsapp/support/experiments
[S30]: https://developers.facebook.com/documentation/business-messaging/whatsapp/support
[S31]: https://developers.facebook.com/documentation/business-messaging/whatsapp/changelog
[S32]: https://developers.facebook.com/documentation/business-messaging/whatsapp/business-scoped-user-ids
[S33]: https://developers.facebook.com/documentation/business-messaging/whatsapp/calling/troubleshooting
[S34]: https://developers.facebook.com/documentation/business-messaging/whatsapp/calling/sandbox
[S35]: https://developers.facebook.com/documentation/business-messaging/whatsapp/calling/call-button-messages-deep-links
[S36]: https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/marketing-templates/media-card-carousel-templates
[S38]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/image-messages
[S39]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/document-messages
[S40]: https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/mark-message-as-read
[S41]: https://developers.facebook.com/documentation/business-messaging/whatsapp/typing-indicators
[G1]: https://developers.facebook.com/documentation/development/graph-api/changelog
[G2]: https://developers.facebook.com/documentation/development/graph-api/changelog/version27.0
[G3]: https://developers.facebook.com/docs/graph-api/guides/versioning
[G4]: https://developers.facebook.com/docs/graph-api/guides/error-handling
[G5]: https://developers.facebook.com/docs/graph-api/webhooks/getting-started
[G6]: https://developers.facebook.com/docs/graph-api/results
[G7]: https://developers.facebook.com/documentation/business-messaging/messenger-platform/webhooks
[O1]: https://raw.githubusercontent.com/meta/openapi/main/business-messaging-api_v23.0.yaml
[O2]: https://github.com/meta/openapi/commits/main
[O3]: https://github.com/fbsamples/whatsapp-api-examples/blob/main/signature-validation-with-webhooks-payloads/app.py
[O4]: https://github.com/fbsamples/WhatsApp-Business-API-Postman-Collection
[A1]: https://web.archive.org/web/20251119121645/https://developers.facebook.com/docs/whatsapp/cloud-api/support/error-codes/
[W1]: https://github.com/dgadelha/whaloc/blob/main/docs/SPEC.md
