const $ = (selector) => document.querySelector(selector);
const API = window.__TAURI_INTERNALS__ ? "http://127.0.0.1:11435" : "";
const state = { models: [], messages: [], busy: false, controller: null, history: [] };

function escapeHTML(value) {
  return value.replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
}

async function request(path, options = {}) {
  const response = await fetch(`${API}${path}`, options);
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try { message = (await response.json()).error || message; } catch {}
    throw new Error(message);
  }
  return response;
}

function toast(message) {
  const element = $("#toast");
  element.textContent = message;
  element.classList.remove("hidden");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => element.classList.add("hidden"), 3600);
}

async function refresh() {
  const dot = $("#engine-dot");
  const status = $("#engine-status");
  try {
    const health = await (await request("/api/v1/health")).json();
    dot.className = `engine-dot ${health.available ? "online" : "offline"}`;
    status.textContent = health.available ? "llama.cpp ready" : "Engine not installed";
    $("#engine-detail").textContent = health.available ? "Local inference · CPU / GPU" : health.message;
    const data = await (await request("/api/v1/models")).json();
    state.models = data.models || [];
    renderModels();
    if (!state.models.some((model) => model.name === $("#chat-model").value)) {
      $("#chat-model").value = health.loadedModel || state.models[0]?.name || "";
    }
    return health;
  } catch (error) {
    dot.className = "engine-dot offline";
    status.textContent = "Server disconnected";
    $("#engine-detail").textContent = error.message;
    return null;
  }
}

function renderModels() {
  $("#model-count").textContent = state.models.length;
  $("#model-total").textContent = `${state.models.length} ${state.models.length === 1 ? "model" : "models"}`;
  $("#empty-models").classList.toggle("hidden", state.models.length > 0);
  $("#model-grid").innerHTML = state.models.map((model) => `
    <article class="model-card ${model.name === $("#chat-model").value ? "selected" : ""}">
      <div class="model-card-top"><div class="model-glyph">✳</div><div><div class="model-name">${escapeHTML(model.name)}</div><div class="model-size">${formatSize(model.size)} · GGUF</div></div></div>
      <div class="model-card-actions"><button class="model-action primary" data-use="${escapeHTML(model.name)}">Usar en chat →</button></div>
    </article>`).join("");
  $("#chat-model").innerHTML = `<option value="">Choose a model</option>` + state.models
    .map((model) => `<option value="${escapeHTML(model.name)}">${escapeHTML(model.name)} · ${formatSize(model.size)}</option>`).join("");
  if (state.models.some((model) => model.name === $("#chat-model").dataset.current)) {
    $("#chat-model").value = $("#chat-model").dataset.current;
  } else if (state.models.length) {
    $("#chat-model").value = state.models[0].name;
  }
  $("#chat-model").dataset.current = $("#chat-model").value;
}

function formatSize(bytes = 0) {
  return bytes >= 1e9 ? `${(bytes / 1e9).toFixed(1)} GB` : `${Math.max(1, Math.round(bytes / 1e6))} MB`;
}

function showView(view) {
  const models = view === "models";
  $("#chat-view").classList.toggle("hidden", models);
  $("#models-view").classList.toggle("hidden", !models);
  $("#nav-chat").classList.toggle("active", !models);
  $("#nav-models").classList.toggle("active", models);
  $("#top-title").textContent = models ? "Models" : "Chat";
  $("#sidebar").classList.remove("open");
}

function openDownload() {
  $("#download-modal").classList.remove("hidden");
  $("#modal-error").classList.add("hidden");
  $("#model-url").focus();
}

function renderMessage(role, text, streaming = false) {
  const row = document.createElement("div");
  row.className = `message ${role}`;
  const content = escapeHTML(text).replace(/```([\s\S]*?)```/g, "<pre>$1</pre>").replace(/\n/g, "<br>");
  row.innerHTML = `<div class="avatar">${role === "assistant" ? "✳" : "●"}</div><div class="message-body">${role === "assistant" ? '<div class="message-meta">LUMBRE</div>' : ""}<span>${content || (streaming ? '<span class="typing"><i></i><i></i><i></i></span>' : "")}</span></div>`;
  $("#messages").append(row);
  row.scrollIntoView({ block: "end", behavior: "smooth" });
  return row.querySelector(".message-body span:last-child");
}

async function sendMessage(text = $("#prompt").value.trim()) {
  const model = $("#chat-model").value;
  if (!text || state.busy) return;
  if (!model) {
    showView("models");
    toast("Download a model before starting a chat.");
    return;
  }
  $("#welcome").classList.add("hidden");
  $("#prompt").value = "";
  $("#prompt").style.height = "auto";
  state.messages.push({ role: "user", content: text });
  renderMessage("user", text);
  const answerNode = renderMessage("assistant", "", true);
  state.busy = true;
  $("#send").textContent = "■";
  $("#send").setAttribute("aria-label", "Stop generation");
  state.controller = new AbortController();
  let answer = "";
  try {
    const response = await request("/v1/chat/completions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model, messages: state.messages, stream: true }),
      signal: state.controller.signal,
    });
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (true) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
      const lines = buffer.split("\n");
      buffer = lines.pop();
      for (const line of lines) {
        if (!line.startsWith("data:")) continue;
        const data = line.slice(5).trim();
        if (!data || data === "[DONE]") continue;
        const chunk = JSON.parse(data);
        if (chunk.error) throw new Error(chunk.error.message || "Failed to generate a response");
        answer += chunk.choices?.[0]?.delta?.content || "";
        answerNode.textContent = answer;
        answerNode.parentElement.scrollIntoView({ block: "end", behavior: "smooth" });
      }
      if (done) break;
    }
    if (answer) state.messages.push({ role: "assistant", content: answer });
    if (!state.history.length) addHistory(text);
  } catch (error) {
    if (error.name !== "AbortError") {
      answerNode.textContent = `Could not generate a response: ${error.message}`;
      toast(error.message);
    }
  } finally {
    state.busy = false;
    state.controller = null;
    $("#send").textContent = "↑";
    $("#send").setAttribute("aria-label", "Send message");
  }
}

function addHistory(title) {
  const item = { title: title.slice(0, 42), messages: [...state.messages] };
  state.history.unshift(item);
  const history = $("#history");
  history.innerHTML = "";
  state.history.slice(0, 12).forEach((entry, index) => {
    const button = document.createElement("button");
    button.className = "history-item";
    button.textContent = entry.title;
    button.addEventListener("click", () => {
      state.messages = [...entry.messages];
      renderConversation();
    });
    button.dataset.index = index;
    history.append(button);
  });
}

function renderConversation() {
  $("#messages").innerHTML = "";
  $("#welcome").classList.add("hidden");
  state.messages.forEach((message) => renderMessage(message.role, message.content));
  showView("chat");
}

function newChat() {
  if (state.busy) state.controller?.abort();
  state.messages = [];
  $("#messages").innerHTML = "";
  $("#welcome").classList.remove("hidden");
  showView("chat");
}

async function downloadModel() {
  const url = $("#model-url").value.trim();
  const button = $("#download-confirm");
  $("#modal-error").classList.add("hidden");
  if (!url) {
    $("#modal-error").textContent = "Paste a direct link to a GGUF file.";
    $("#modal-error").classList.remove("hidden");
    return;
  }
  button.disabled = true;
  button.innerHTML = "Downloading… <span>↻</span>";
  $("#download-progress").classList.remove("hidden");
  $("#download-name").textContent = url.split("/").pop().split("?")[0] || "Model";
  try {
    const response = await request("/api/v1/models/download", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ url }),
    });
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (true) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
      const lines = buffer.split("\n");
      buffer = lines.pop();
      for (const line of lines) {
        if (!line.trim()) continue;
        const progress = JSON.parse(line);
        if (progress.error) throw new Error(progress.error);
        $("#download-name").textContent = progress.status || "Downloading…";
        $("#download-percent").textContent = `${Math.round(progress.progress)}%`;
        $("#progress-bar").style.width = `${Math.max(0, Math.min(100, progress.progress))}%`;
      }
      if (done) break;
    }
    $("#download-modal").classList.add("hidden");
    $("#model-url").value = "";
    await refresh();
    toast("Model downloaded. You can start chatting.");
    showView("models");
  } catch (error) {
    $("#modal-error").textContent = error.message;
    $("#modal-error").classList.remove("hidden");
  } finally {
    button.disabled = false;
    button.innerHTML = 'Download model <span>→</span>';
  }
}

$("#new-chat").addEventListener("click", newChat);
$("#nav-chat").addEventListener("click", () => showView("chat"));
$("#nav-models").addEventListener("click", () => showView("models"));
$("#setup-cta").addEventListener("click", () => showView("models"));
$("#open-download").addEventListener("click", openDownload);
$("#empty-download").addEventListener("click", openDownload);
$("#close-modal").addEventListener("click", () => $("#download-modal").classList.add("hidden"));
$("#download-modal").addEventListener("click", (event) => {
  if (event.target.id === "download-modal") $("#download-modal").classList.add("hidden");
});
$("#download-confirm").addEventListener("click", downloadModel);
$("#refresh").addEventListener("click", refresh);
$("#mobile-menu").addEventListener("click", () => $("#sidebar").classList.toggle("open"));
$("#chat-model").addEventListener("change", (event) => { event.target.dataset.current = event.target.value; renderModels(); });
$("#model-grid").addEventListener("click", (event) => {
  const button = event.target.closest("[data-use]");
  if (!button) return;
  $("#chat-model").value = button.dataset.use;
  $("#chat-model").dataset.current = button.dataset.use;
  showView("chat");
  toast(`${button.dataset.use} is ready to use.`);
});
$("#send").addEventListener("click", () => {
  if (state.busy) state.controller?.abort();
  else sendMessage();
});
$("#prompt").addEventListener("input", (event) => {
  event.target.style.height = "auto";
  event.target.style.height = `${Math.min(event.target.scrollHeight, 150)}px`;
});
$("#prompt").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); sendMessage(); }
});
document.querySelectorAll(".suggestion").forEach((button) => button.addEventListener("click", () => sendMessage(button.dataset.prompt)));
$("#theme-toggle").addEventListener("click", () => {
  const light = document.body.classList.toggle("light");
  document.documentElement.style.colorScheme = light ? "light" : "dark";
});
document.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); newChat(); }
  if (event.key === "Escape") $("#download-modal").classList.add("hidden");
});

refresh();
setInterval(refresh, 30000);
if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(() => {});
