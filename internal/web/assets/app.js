const state = {
  email: "",
  conversations: [],
  apps: [],
  activeId: null,
  messages: [],
  busy: false,
  view: "chat",
  pendingDelete: null,
};

const el = (id) => document.getElementById(id);

const transcript = el("transcript");
const chatView = el("view-chat");
const appsView = el("view-apps");
const empty = el("empty");
const draft = el("draft");
const sendButton = el("send");
const banner = el("banner");
const composer = el("composer");
const sidebar = el("sidebar");
const scrim = el("scrim");
const menu = el("menu");
const owner = el("owner");
const conversations = el("conversations");
const appList = el("app-list");
const keyPanel = el("key-panel");
const keyValue = el("key-value");
const confirmDialog = el("confirm");

const icons = {
  trash: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/></svg>',
  sun: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>',
  moon: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/></svg>',
  system: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="8"/><path d="M12 4a8 8 0 0 1 0 16z" fill="currentColor" stroke="none"/></svg>',
};

async function api(path, options = {}) {
  const res = await fetch(path, { credentials: "same-origin", ...options });
  if (!res.ok) {
    throw Object.assign(new Error(await errorText(res)), { status: res.status });
  }
  if (res.status === 204) return null;
  return res.json();
}

async function errorText(res) {
  try {
    const data = await res.json();
    if (typeof data.error === "string" && data.error) return data.error;
  } catch {
    // Fall through to the status message.
  }
  return `Request failed (${res.status}).`;
}

function showBanner(message) {
  banner.textContent = message;
  banner.hidden = false;
}

function hideBanner() {
  banner.hidden = true;
  banner.textContent = "";
}

function relativeTime(iso) {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  if (seconds < 604800) return `${Math.floor(seconds / 86400)}d ago`;
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(then);
}

function setBusy(busy) {
  state.busy = busy;
  composer.setAttribute("aria-busy", String(busy));
  sendButton.textContent = busy ? "Sending..." : "Send";
  sendButton.dataset.pending = String(busy);
  updateSendState();
}

function updateSendState() {
  sendButton.disabled = state.busy || draft.value.trim().length === 0;
}

function autoResize() {
  draft.style.height = "auto";
  const limit = parseFloat(getComputedStyle(draft).maxHeight);
  const height = Number.isFinite(limit) ? Math.min(draft.scrollHeight, limit) : draft.scrollHeight;
  draft.style.height = `${height}px`;
}

function nearBottom() {
  return transcript.scrollHeight - transcript.scrollTop - transcript.clientHeight < 120;
}

function scrollToBottom(force = false) {
  if (force || nearBottom()) {
    transcript.scrollTop = transcript.scrollHeight;
  }
}

function messageNode(role, text, streaming = false) {
  const article = document.createElement("article");
  article.className = `msg msg--${role}${streaming ? " is-streaming" : ""}`;
  const speaker = document.createElement("span");
  speaker.className = "msg__speaker";
  speaker.textContent = role === "user" ? "You" : "Assistant";
  const body = document.createElement("div");
  body.className = "msg__body";
  const paragraph = document.createElement("p");
  paragraph.className = "msg__text";
  paragraph.textContent = text;
  body.appendChild(paragraph);
  article.append(speaker, body);
  return { node: article, text: paragraph };
}

function renderMessages() {
  transcript.replaceChildren();
  if (state.messages.length === 0) {
    chatView.classList.remove("has-messages");
    empty.hidden = false;
    return;
  }
  chatView.classList.add("has-messages");
  empty.hidden = true;
  for (const message of state.messages) {
    transcript.appendChild(messageNode(message.role, message.content).node);
  }
  scrollToBottom(true);
}

function renderConversations() {
  conversations.replaceChildren();
  if (state.conversations.length === 0) {
    const note = document.createElement("p");
    note.className = "empty-note conversations__empty";
    note.textContent = "No chats yet.";
    conversations.appendChild(note);
    return;
  }
  for (const conversation of state.conversations) {
    const item = document.createElement("div");
    item.className = `conv${conversation.id === state.activeId ? " is-active" : ""}`;

    const open = document.createElement("button");
    open.type = "button";
    open.className = "conv__open";
    open.addEventListener("click", () => openConversation(conversation.id));

    const title = document.createElement("span");
    title.className = "conv__title";
    title.textContent = conversation.title;
    const time = document.createElement("span");
    time.className = "conv__time";
    time.textContent = relativeTime(conversation.updated_at);
    open.append(title, time);

    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "conv__del";
    remove.setAttribute("aria-label", `Delete ${conversation.title}`);
    remove.innerHTML = icons.trash;
    remove.addEventListener("click", () => askDelete("conversation", conversation.id, conversation.title));

    item.append(open, remove);
    conversations.appendChild(item);
  }
}

async function loadMe() {
  try {
    const data = await api("/api/me");
    state.email = data.email || "";
    owner.textContent = state.email;
    owner.title = state.email;
  } catch (err) {
    showBanner(err.status === 503 ? "UI auth is not configured on this server yet." : err.message);
    draft.disabled = true;
    sendButton.disabled = true;
    throw err;
  }
}

async function loadConversations() {
  try {
    const data = await api("/api/conversations");
    state.conversations = data.conversations || [];
    renderConversations();
  } catch (err) {
    showBanner(err.message);
  }
}

async function openConversation(id) {
  state.activeId = id;
  state.messages = [];
  showView("chat");
  renderConversations();
  renderMessages();
  closeNav();
  try {
    const data = await api(`/api/conversations/${encodeURIComponent(id)}/messages`);
    if (state.activeId !== id) return;
    state.messages = data.messages || [];
    renderMessages();
  } catch (err) {
    showBanner(err.message);
  }
}

function newChat() {
  state.activeId = null;
  state.messages = [];
  showView("chat");
  renderConversations();
  renderMessages();
  hideBanner();
  closeNav();
  draft.focus();
}

async function sendMessage() {
  const content = draft.value.trim();
  if (!content || state.busy) return;
  hideBanner();
  setBusy(true);
  draft.value = "";
  autoResize();

  if (!state.activeId) {
    try {
      const data = await api("/api/conversations", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: "{}",
      });
      state.activeId = data.conversation.id;
      state.conversations.unshift(data.conversation);
      renderConversations();
    } catch (err) {
      draft.value = content;
      autoResize();
      updateSendState();
      setBusy(false);
      showBanner(err.message);
      return;
    }
  }

  state.messages.push({ role: "user", content });
  renderMessages();
  scrollToBottom(true);

  const stream = messageNode("assistant", "", true);
  transcript.appendChild(stream.node);
  scrollToBottom(true);
  el("announcer").textContent = "Assistant is replying.";

  const controller = new AbortController();
  let accumulated = "";
  let done = null;
  try {
    const res = await fetch(`/api/conversations/${encodeURIComponent(state.activeId)}/messages`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ content }),
      signal: controller.signal,
    });
    if (!res.ok) {
      throw new Error(await errorText(res));
    }
    await readSSE(res, (event, data) => {
      if (event === "delta" && typeof data.text === "string") {
        accumulated += data.text;
        stream.text.textContent = accumulated;
        stream.node.classList.add("is-streaming");
        scrollToBottom();
      } else if (event === "error") {
        throw new Error(data.error || "The model did not answer.");
      } else if (event === "done") {
        done = data;
      }
    });
    if (!done) throw new Error("The model did not answer.");

    state.messages.pop();
    state.messages.push(done.user, done.assistant);
    const conversation = state.conversations.find((c) => c.id === state.activeId);
    if (conversation) {
      conversation.title = done.title;
      conversation.updated_at = new Date().toISOString();
    }
    renderConversations();
    renderMessages();
    scrollToBottom(true);
    el("announcer").textContent = `Assistant replied: ${done.reply}`;
  } catch (err) {
    controller.abort();
    state.messages.pop();
    stream.node.remove();
    renderMessages();
    draft.value = content;
    autoResize();
    updateSendState();
    el("announcer").textContent = "";
    showBanner(err.message);
  } finally {
    setBusy(false);
    draft.focus();
  }
}

async function readSSE(response, onEvent) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let index;
    while ((index = buffer.indexOf("\n\n")) >= 0) {
      const frame = buffer.slice(0, index);
      buffer = buffer.slice(index + 2);
      let event = "message";
      let data = "";
      for (const line of frame.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data += line.slice(5).trim();
      }
      if (data) onEvent(event, JSON.parse(data));
    }
  }
}

function showView(view) {
  state.view = view;
  const isApps = view === "apps";
  chatView.hidden = isApps;
  appsView.hidden = !isApps;
  el("apps-btn").setAttribute("aria-pressed", String(isApps));
  if (isApps) {
    loadApps();
  } else {
    keyPanel.hidden = true;
    keyValue.textContent = "";
  }
  closeNav();
}

async function loadApps() {
  try {
    const data = await api("/api/apps");
    state.apps = data.apps || [];
    renderApps();
  } catch (err) {
    showBanner(err.message);
  }
}

function renderApps() {
  appList.replaceChildren();
  if (state.apps.length === 0) {
    const note = document.createElement("li");
    note.className = "empty-note";
    note.textContent = "No apps yet. Create a key to call this service from another application.";
    appList.appendChild(note);
    return;
  }
  for (const app of state.apps) {
    const row = document.createElement("li");
    row.className = "app-row";

    const main = document.createElement("div");
    main.className = "app-row__main";
    const name = document.createElement("p");
    name.className = "app-row__name";
    name.textContent = app.name;
    const meta = document.createElement("p");
    meta.className = "app-row__meta";
    const last = app.last_used_at ? `, last used ${relativeTime(app.last_used_at)}` : "";
    meta.textContent = `${app.key_prefix}... created ${relativeTime(app.created_at)}${last}`;
    main.append(name, meta);

    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "app-row__del";
    remove.textContent = "Delete";
    remove.setAttribute("aria-label", `Delete app ${app.name}`);
    remove.addEventListener("click", () => askDelete("app", app.id, app.name));

    row.append(main, remove);
    appList.appendChild(row);
  }
}

async function createApp(event) {
  event.preventDefault();
  hideBanner();
  const name = el("app-name").value.trim();
  const prompt = el("app-prompt").value.trim();
  if (!name) return;
  const button = el("create-app");
  button.disabled = true;
  button.textContent = "Creating...";
  try {
    const data = await api("/api/apps", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ name, system_prompt: prompt }),
    });
    state.apps.unshift(data.app);
    renderApps();
    el("app-form").reset();
    keyValue.textContent = data.key;
    keyPanel.hidden = false;
    el("copy-key").textContent = "Copy key";
    keyPanel.scrollIntoView({ block: "nearest" });
  } catch (err) {
    showBanner(err.message);
  } finally {
    button.disabled = false;
    button.textContent = "Create key";
  }
}

function askDelete(kind, id, label) {
  state.pendingDelete = { kind, id, label };
  confirmDialog.returnValue = "";
  el("confirm-title").textContent = kind === "app" ? "Delete this app?" : "Delete this chat?";
  el("confirm-text").textContent =
    kind === "app"
      ? `${label} will lose access immediately. This cannot be undone.`
      : `${label} and its messages will be removed from this homelab.`;
  confirmDialog.showModal();
}

async function performDelete() {
  const pending = state.pendingDelete;
  state.pendingDelete = null;
  if (!pending) return;
  try {
    if (pending.kind === "conversation") {
      await api(`/api/conversations/${encodeURIComponent(pending.id)}`, { method: "DELETE" });
      state.conversations = state.conversations.filter((c) => c.id !== pending.id);
      if (state.activeId === pending.id) {
        state.activeId = null;
        state.messages = [];
        renderMessages();
      }
      renderConversations();
    } else {
      await api(`/api/apps/${encodeURIComponent(pending.id)}`, { method: "DELETE" });
      state.apps = state.apps.filter((a) => a.id !== pending.id);
      renderApps();
    }
  } catch (err) {
    showBanner(err.message);
  }
}

const DESKTOP_MIN_WIDTH = 900;

function syncSidebarInert() {
  sidebar.inert = window.innerWidth < DESKTOP_MIN_WIDTH && !document.body.classList.contains("nav-open");
}

function openNav() {
  document.body.classList.add("nav-open");
  menu.setAttribute("aria-expanded", "true");
  scrim.hidden = false;
  syncSidebarInert();
}

function closeNav() {
  document.body.classList.remove("nav-open");
  menu.setAttribute("aria-expanded", "false");
  scrim.hidden = true;
  syncSidebarInert();
}

function toggleNav() {
  if (document.body.classList.contains("nav-open")) closeNav();
  else openNav();
}

function applyTheme(theme) {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("chat-theme", theme);
  } else {
    delete document.documentElement.dataset.theme;
    localStorage.removeItem("chat-theme");
  }
  updateThemeButton();
}

function effectiveTheme() {
  const explicit = document.documentElement.dataset.theme;
  if (explicit === "light" || explicit === "dark") return explicit;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function updateThemeButton() {
  const explicit = document.documentElement.dataset.theme;
  const button = el("theme");
  if (explicit === "dark") {
    button.innerHTML = icons.moon;
    button.setAttribute("aria-label", "Theme: dark. Switch to light.");
  } else if (explicit === "light") {
    button.innerHTML = icons.sun;
    button.setAttribute("aria-label", "Theme: light. Follow the system.");
  } else {
    button.innerHTML = icons.system;
    button.setAttribute("aria-label", `Theme: system (${effectiveTheme()}). Switch to dark.`);
  }
}

function cycleTheme() {
  const explicit = document.documentElement.dataset.theme;
  if (explicit === "dark") applyTheme("light");
  else if (explicit === "light") applyTheme("system");
  else applyTheme("dark");
}

function boot() {
  updateThemeButton();

  el("brand").addEventListener("click", (event) => {
    event.preventDefault();
    newChat();
  });
  el("new-chat").addEventListener("click", newChat);
  el("apps-btn").addEventListener("click", () => {
    showView(state.view === "apps" ? "chat" : "apps");
  });
  el("back-chat").addEventListener("click", () => showView("chat"));
  menu.addEventListener("click", toggleNav);
  scrim.addEventListener("click", closeNav);
  el("theme").addEventListener("click", cycleTheme);

  composer.addEventListener("submit", (event) => {
    event.preventDefault();
    sendMessage();
  });
  draft.addEventListener("input", () => {
    autoResize();
    updateSendState();
  });
  draft.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      sendMessage();
    }
  });

  for (const prompt of document.querySelectorAll(".prompt")) {
    prompt.addEventListener("click", () => {
      draft.value = prompt.dataset.prompt || "";
      autoResize();
      updateSendState();
      draft.focus();
    });
  }

  el("app-form").addEventListener("submit", createApp);
  el("copy-key").addEventListener("click", async () => {
    const value = keyValue.textContent || "";
    try {
      await navigator.clipboard.writeText(value);
      el("copy-key").textContent = "Copied";
      setTimeout(() => {
        el("copy-key").textContent = "Copy key";
      }, 1600);
    } catch {
      const range = document.createRange();
      range.selectNodeContents(keyValue);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    }
  });

  confirmDialog.addEventListener("close", () => {
    if (confirmDialog.returnValue === "confirm") {
      performDelete();
    } else {
      state.pendingDelete = null;
    }
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeNav();
    }
  });

  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (!document.documentElement.dataset.theme) updateThemeButton();
  });
  window.addEventListener("resize", syncSidebarInert);

  renderMessages();
  renderConversations();
  autoResize();
  updateSendState();
  syncSidebarInert();

  loadMe()
    .then(loadConversations)
    .catch(() => {});
}

boot();
