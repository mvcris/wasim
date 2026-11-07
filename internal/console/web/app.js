"use strict";

// Every value from the API reaches the page through text nodes (see el), so a
// message body can never inject markup.

const state = {
  info: null,
  contacts: [],
  selected: null,
  messages: [],
  deliveries: [],
  rules: [],
  errors: [],
  quote: null,
  openLists: new Set(),
  openRaw: new Set(),
  openDeliveries: new Set(),
};

const $ = (id) => document.getElementById(id);

// Follows new messages unless the user scrolled up to read older ones. Kept as
// state rather than measured on render, because an image finishing its load
// grows the list after the render that added it.
let followLatest = true;

function scrollToLatest() {
  if (followLatest) $("messages").scrollTop = $("messages").scrollHeight;
}

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (key === "class") node.className = value;
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else if (value !== undefined && value !== null && value !== false) node.setAttribute(key, value);
  }
  for (const child of children.flat()) {
    if (child !== null && child !== undefined && child !== false) node.append(child);
  }
  return node;
}

async function api(path, options = {}) {
  const init = { method: options.method ?? "GET" };
  if (options.form) {
    init.body = options.form;
  } else if (options.body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(options.body);
  }
  const response = await fetch(path, init);
  // The session expired, was logged out elsewhere, or the user was removed.
  if (response.status === 401) {
    window.location.assign("/login");
    throw new Error("Your session ended. Log in again.");
  }
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    throw new Error(data?.error ?? `Request failed with HTTP ${response.status}`);
  }
  return data;
}

let toastTimer;
function toast(message, isError = false) {
  const node = $("toast");
  node.textContent = message;
  node.classList.toggle("error", isError);
  node.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    node.hidden = true;
  }, isError ? 7000 : 3000);
}

// Runs a user action and surfaces its failure, so no click fails silently.
async function run(action) {
  try {
    await action();
  } catch (error) {
    toast(error.message, true);
  }
}

const timeOf = (iso) => new Date(iso).toLocaleTimeString();
const contactPath = (waID) => `/api/contacts/${encodeURIComponent(waID)}`;

async function refresh() {
  const [contacts, deliveries, rules] = await Promise.all([
    api("/api/contacts"),
    api("/api/deliveries"),
    api("/api/failure-rules"),
  ]);
  state.contacts = contacts;
  state.deliveries = deliveries;
  state.rules = rules;
  if (!state.selected && contacts.length > 0) state.selected = contacts[0].wa_id;
  state.messages = state.selected ? await api(`${contactPath(state.selected)}/messages`) : [];
  render();
}

// Bursts of status webhooks signal many times; one refetch covers them all.
let refreshQueued = false;
function scheduleRefresh() {
  if (refreshQueued) return;
  refreshQueued = true;
  setTimeout(() => {
    refreshQueued = false;
    run(refresh);
  }, 60);
}

function render() {
  renderContacts();
  renderChatHeader();
  renderMessages();
  renderDeliveries();
  renderRules();
  $("composer").hidden = !state.selected;
}

function renderContacts() {
  $("contact-list").replaceChildren(
    ...state.contacts.map((contact) =>
      el(
        "li",
        {},
        el(
          "button",
          {
            type: "button",
            "aria-current": contact.wa_id === state.selected ? "true" : null,
            onclick: () => selectContact(contact.wa_id),
          },
          el("span", { class: "contact-name" }, contact.name),
          el("span", { class: "contact-phone" }, `+${contact.wa_id}`),
        ),
      ),
    ),
  );
}

function selectContact(waID) {
  state.selected = waID;
  state.quote = null;
  followLatest = true;
  renderQuote();
  run(refresh);
}

function renderChatHeader() {
  const contact = state.contacts.find((item) => item.wa_id === state.selected);
  if (!contact) {
    $("chat-header").replaceChildren(el("span", { class: "muted" }, "Add a user to start a conversation"));
    return;
  }
  $("chat-header").replaceChildren(
    el(
      "div",
      { class: "chat-title" },
      el("strong", {}, contact.name),
      " ",
      el("span", { class: "muted small" }, `+${contact.wa_id}`),
    ),
    windowBadge(contact),
    el(
      "button",
      {
        type: "button",
        disabled: !contact.window_open,
        title: "Close the 24h customer service window now",
        onclick: () =>
          run(async () => {
            await api(`${contactPath(contact.wa_id)}/window/expire`, { method: "POST" });
            toast("24h window closed");
            await refresh();
          }),
      },
      "Expire window",
    ),
  );
}

function windowBadge(contact) {
  if (!state.info?.enforce_window) return el("span", { class: "badge" }, "24h window not enforced");
  return contact.window_open
    ? el("span", { class: "badge ok" }, "24h window open")
    : el("span", { class: "badge warn" }, "24h window closed");
}

function renderMessages() {
  const container = $("messages");
  if (!state.selected) {
    container.replaceChildren(el("p", { class: "empty" }, "Select or add a user on the left."));
    return;
  }
  if (state.messages.length === 0) {
    container.replaceChildren(
      el("p", { class: "empty" }, "No messages yet. Type below to send one to your app as this user, or send one from your app to this number."),
    );
    return;
  }
  const byID = new Map(state.messages.map((message) => [message.id, message]));
  container.replaceChildren(...state.messages.map((message) => renderBubble(message, byID)));
  scrollToLatest();
}

function previewText(message) {
  const rendered = message.rendered;
  return rendered.body || rendered.header || `[${message.type}]`;
}

function renderBubble(message, byID) {
  const fromUser = message.direction === "inbound";
  const rendered = message.rendered;
  const quoted = message.context_id ? byID.get(message.context_id) : null;
  return el(
    "div",
    { class: `bubble ${fromUser ? "user" : "business"}` },
    rendered.template_name && el("span", { class: "bubble-label" }, `Template: ${rendered.template_name}`),
    message.context_id && el("div", { class: "bubble-quote" }, quoted ? previewText(quoted) : "Quoted message"),
    message.media_id && renderAttachment(message),
    rendered.header && el("div", { class: "bubble-header" }, rendered.header),
    rendered.body && el("div", { class: "bubble-body" }, rendered.body),
    rendered.footer && el("div", { class: "bubble-footer" }, rendered.footer),
    !fromUser && renderOptions(message),
    message.error_code ? el("div", { class: "bubble-error" }, `Failed: (#${message.error_code}) ${message.error_title}`) : null,
    renderBubbleMeta(message, fromUser),
    state.openRaw.has(message.id) && el("pre", { class: "code raw" }, JSON.stringify(message.raw, null, 2)),
  );
}

function renderAttachment(message) {
  const url = `/api/media/${encodeURIComponent(message.media_id)}`;
  if (message.type === "image") {
    return el("img", { class: "bubble-image", src: url, alt: message.rendered.body || "Attached image", onload: scrollToLatest });
  }
  const filename = message.raw?.document?.filename ?? "document";
  return el("a", { class: "bubble-file", href: url, target: "_blank", rel: "noopener" }, filename);
}

function renderOptions(message) {
  const { buttons = [], list } = message.rendered;
  const options = buttons.map((button) =>
    el(
      "button",
      {
        type: "button",
        disabled: button.kind === "link",
        title: button.kind === "link" ? "Link buttons produce no webhook" : "Tap as this user",
        onclick: () => tapOption(message.id, button.id),
      },
      button.title,
    ),
  );
  if (list) {
    options.push(renderListOptions(message, list));
  }
  return options.length > 0 ? el("div", { class: "bubble-options" }, options) : null;
}

function renderListOptions(message, list) {
  const open = state.openLists.has(message.id);
  const toggle = el(
    "button",
    {
      type: "button",
      "aria-expanded": open ? "true" : "false",
      onclick: () => {
        if (open) state.openLists.delete(message.id);
        else state.openLists.add(message.id);
        renderMessages();
      },
    },
    list.button,
  );
  if (!open) return toggle;
  const rows = list.sections.flatMap((section) => [
    section.title ? el("div", { class: "list-section" }, section.title) : null,
    ...section.rows.map((row) =>
      el(
        "button",
        { type: "button", onclick: () => tapOption(message.id, row.id) },
        el("span", { class: "row-title" }, row.title),
        row.description ? el("span", { class: "row-description" }, row.description) : null,
      ),
    ),
  ]);
  return el("div", { class: "bubble-options" }, toggle, rows);
}

function tapOption(messageID, replyID) {
  run(async () => {
    await api(`${contactPath(state.selected)}/messages`, {
      method: "POST",
      body: { type: "reply", context_id: messageID, reply_id: replyID },
    });
    await refresh();
  });
}

function renderBubbleMeta(message, fromUser) {
  return el(
    "div",
    { class: "bubble-meta" },
    el("span", {}, timeOf(message.timestamp)),
    el("span", { class: statusClass(message.status) }, message.status),
    el("span", { class: "spacer" }),
    el(
      "button",
      {
        type: "button",
        class: "link",
        onclick: () => {
          state.quote = { id: message.id, text: previewText(message) };
          renderQuote();
          $("composer-text").focus();
        },
      },
      "Quote",
    ),
    !fromUser && message.status !== "failed" && renderStatusMenu(message),
    el(
      "button",
      {
        type: "button",
        class: "link",
        onclick: () => {
          if (state.openRaw.has(message.id)) state.openRaw.delete(message.id);
          else state.openRaw.add(message.id);
          renderMessages();
        },
      },
      "JSON",
    ),
  );
}

function statusClass(status) {
  if (status === "failed") return "badge bad";
  if (status === "read" || status === "delivered") return "badge ok";
  return "badge";
}

function renderStatusMenu(message) {
  const select = el(
    "select",
    {
      "aria-label": "Send a status webhook",
      onchange: (event) => {
        const value = event.target.value;
        event.target.value = "";
        if (!value) return;
        const [status, code] = value.split(":");
        run(async () => {
          await api(`/api/messages/${encodeURIComponent(message.id)}/status`, {
            method: "POST",
            body: { status, code: Number(code ?? 0) },
          });
        });
      },
    },
    el("option", { value: "" }, "Status..."),
    el("option", { value: "delivered" }, "Delivered"),
    el("option", { value: "read" }, "Read"),
    state.errors.map((item) => el("option", { value: `failed:${item.code}` }, `Fail ${item.code}`)),
  );
  return select;
}

function renderQuote() {
  const node = $("quote-preview");
  if (!state.quote) {
    node.hidden = true;
    node.replaceChildren();
    return;
  }
  node.hidden = false;
  node.replaceChildren(
    el("span", {}, state.quote.text),
    el(
      "button",
      {
        type: "button",
        class: "link",
        onclick: () => {
          state.quote = null;
          renderQuote();
        },
      },
      "Remove",
    ),
  );
}

function deliverySummary(delivery) {
  try {
    const value = JSON.parse(delivery.payload).entry[0].changes[0].value;
    if (value.statuses) {
      const status = value.statuses[0];
      const code = status.errors?.[0]?.code;
      return `status ${status.status}${code ? ` #${code}` : ""}`;
    }
    if (value.messages) return `message ${value.messages[0].type}`;
  } catch {
    // A webhook is always our own JSON; fall back to its kind if that changes.
  }
  return delivery.kind;
}

function stateBadgeClass(deliveryState) {
  if (deliveryState === "delivered") return "badge ok";
  if (deliveryState === "failed") return "badge bad";
  if (deliveryState === "pending" || deliveryState === "in_flight") return "badge warn";
  return "badge";
}

function renderDeliveries() {
  const list = $("delivery-list");
  if (state.deliveries.length === 0) {
    list.replaceChildren(el("li", { class: "muted small" }, "No webhooks yet."));
    return;
  }
  list.replaceChildren(...state.deliveries.map(renderDelivery));
}

function renderDelivery(delivery) {
  const open = state.openDeliveries.has(delivery.id);
  return el(
    "li",
    { class: "delivery" },
    el(
      "button",
      {
        type: "button",
        class: "delivery-head",
        "aria-expanded": open ? "true" : "false",
        onclick: () => {
          if (open) state.openDeliveries.delete(delivery.id);
          else state.openDeliveries.add(delivery.id);
          renderDeliveries();
        },
      },
      el("span", { class: stateBadgeClass(delivery.state) }, delivery.state),
      el("span", { class: "delivery-summary" }, deliverySummary(delivery)),
      el("span", { class: "muted small" }, timeOf(delivery.created_at)),
    ),
    el(
      "div",
      { class: "delivery-meta" },
      `#${delivery.id}, attempts: ${delivery.attempts}`,
      delivery.last_status ? `, last HTTP ${delivery.last_status}` : "",
      delivery.bad_signature ? ", bad signature" : "",
    ),
    delivery.last_error && el("div", { class: "delivery-meta" }, delivery.last_error),
    open &&
      el(
        "div",
        { class: "delivery-actions" },
        el("button", { type: "button", class: "link", onclick: () => redeliver(delivery.id, false) }, "Send duplicate"),
        el("button", { type: "button", class: "link", onclick: () => redeliver(delivery.id, true) }, "Send with bad signature"),
      ),
    open && el("pre", { class: "code" }, delivery.payload),
  );
}

function redeliver(id, badSignature) {
  run(async () => {
    await api(`/api/deliveries/${id}/redeliver`, { method: "POST", body: { bad_signature: badSignature } });
    toast(badSignature ? "Queued a copy with a wrong signature" : "Queued a duplicate");
  });
}

function renderRules() {
  const list = $("rule-list");
  if (state.rules.length === 0) {
    list.replaceChildren(el("li", { class: "muted small" }, "No rules. Sends behave normally."));
    return;
  }
  list.replaceChildren(
    ...state.rules.map((rule) => {
      const definition = state.errors.find((item) => item.code === rule.code);
      const count = rule.remaining === -1 ? "until removed" : `${rule.remaining} left`;
      const scope = rule.contact_wa_id ? `+${rule.contact_wa_id}` : "everyone";
      return el(
        "li",
        { class: "rule" },
        el("span", {}, `#${rule.code} ${definition?.title ?? ""} (${rule.channel}), ${scope}, ${count}`),
        el(
          "button",
          {
            type: "button",
            class: "link",
            onclick: () => run(() => api(`/api/failure-rules/${rule.id}`, { method: "DELETE" })),
          },
          "Remove",
        ),
      );
    }),
  );
}

function renderSetup() {
  const info = state.info;
  $("version").textContent = info.version;
  $("account").textContent = `phone number id ${info.phone_number_id}, business account ${info.business_account_id}, +${info.display_phone_number}`;
  $("setup-values").textContent = [
    `Graph API base URL   ${info.graph_url}`,
    `Phone number id      ${info.phone_number_id}`,
    `Business account id  ${info.business_account_id}`,
    `Access token         any non-empty value, unless restricted`,
    `Webhook URL          ${info.webhook_url || "(not configured)"}`,
    `Automatic statuses   ${info.auto_statuses.join(", ") || "(none)"}`,
  ].join("\n");
  $("failure-code").replaceChildren(
    ...state.errors.map((item) => el("option", { value: item.code }, `${item.code}: ${item.title}`)),
  );
  $("current-user").textContent = info.user ?? "";
  $("current-user").hidden = !info.user;
  $("logout").hidden = !info.user;
}

function bindEvents() {
  $("messages").addEventListener("scroll", () => {
    const list = $("messages");
    followLatest = list.scrollHeight - list.scrollTop - list.clientHeight < 60;
  });

  $("contact-form").addEventListener("submit", (event) => {
    event.preventDefault();
    run(async () => {
      const contact = await api("/api/contacts", {
        method: "POST",
        body: { wa_id: $("contact-phone").value, name: $("contact-name").value },
      });
      event.target.reset();
      state.selected = contact.wa_id;
      await refresh();
    });
  });

  $("composer").addEventListener("submit", (event) => {
    event.preventDefault();
    run(sendFromComposer);
  });

  $("attachment").addEventListener("change", () => {
    const file = $("attachment").files[0];
    $("attachment-name").hidden = !file;
    $("attachment-name").textContent = file ? `Attached: ${file.name} (text below becomes the caption)` : "";
  });

  $("logout").addEventListener("click", () =>
    run(async () => {
      await api("/api/logout", { method: "POST" });
      window.location.assign("/login");
    }),
  );

  $("verify-webhook").addEventListener("click", () =>
    run(async () => {
      const result = await api("/api/webhook/verify", { method: "POST" });
      if (result.verified) toast("Webhook verified: your app echoed the challenge");
      else toast(`Verification failed: ${result.error}${result.status ? ` (HTTP ${result.status})` : ""}`, true);
    }),
  );

  $("reset").addEventListener("click", () => {
    if (!window.confirm("Delete all conversations, media, rules and webhooks?")) return;
    run(async () => {
      await api("/api/reset", { method: "POST" });
      state.selected = null;
      state.quote = null;
      renderQuote();
      await refresh();
    });
  });

  $("failure-form").addEventListener("submit", (event) => {
    event.preventDefault();
    run(async () => {
      const scoped = $("failure-scope").value === "contact";
      if (scoped && !state.selected) throw new Error("Select a user first, or apply the rule to every user");
      await api("/api/failure-rules", {
        method: "POST",
        body: {
          contact_wa_id: scoped ? state.selected : "",
          code: Number($("failure-code").value),
          channel: $("failure-channel").value,
          times: Number($("failure-times").value),
        },
      });
      event.target.reset();
      toast("Rule added");
      await refresh();
    });
  });

  for (const tab of document.querySelectorAll("[data-tab]")) {
    tab.addEventListener("click", () => selectTab(tab.dataset.tab));
  }
}

async function sendFromComposer() {
  const text = $("composer-text").value;
  const file = $("attachment").files[0];
  const path = `${contactPath(state.selected)}/messages`;
  if (!text && !file) return;
  if (file) {
    const form = new FormData();
    form.append("file", file);
    form.append("caption", text);
    if (state.quote) form.append("context_id", state.quote.id);
    await api(path, { method: "POST", form });
  } else {
    await api(path, {
      method: "POST",
      body: {
        type: "text",
        text,
        context_id: state.quote?.id ?? "",
        hours_ago: Number($("composer-hours").value || 0),
      },
    });
  }
  $("composer").reset();
  $("attachment-name").hidden = true;
  state.quote = null;
  followLatest = true;
  renderQuote();
  await refresh();
  $("composer-text").focus();
}

function selectTab(name) {
  for (const tab of document.querySelectorAll("[data-tab]")) {
    tab.setAttribute("aria-selected", tab.dataset.tab === name ? "true" : "false");
  }
  for (const panel of document.querySelectorAll("[data-panel]")) {
    panel.hidden = panel.dataset.panel !== name;
  }
}

function connectEvents() {
  const stream = new EventSource("/api/events");
  stream.onopen = () => {
    $("connection").textContent = "live";
    $("connection").classList.add("live");
    scheduleRefresh();
  };
  stream.onmessage = scheduleRefresh;
  stream.onerror = () => {
    $("connection").textContent = "offline";
    $("connection").classList.remove("live");
  };
}

async function start() {
  [state.info, state.errors] = await Promise.all([api("/api/info"), api("/api/errors")]);
  renderSetup();
  bindEvents();
  await refresh();
  connectEvents();
}

run(start);
