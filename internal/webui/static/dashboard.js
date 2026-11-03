// The agent dashboard: sessions grouped by what they need from you, with
// a peek at the last message, an inline reply, stop, pin, and a dispatch
// box that starts a background run. Talks to /api/dashboard and
// /api/dashboard/{reply,stop,dispatch}; pin reuses /api/session/pin.
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);
  const esc = (t) => String(t == null ? "" : t).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const root = $("dashboard");
  if (!root) return;
  const body = $("dash-body");
  const note = $("dash-note");
  const GROUPS = [
    ["awaiting", "Needs you", "Waiting on an answer or a decision"],
    ["working", "Working", "A turn or background run is going"],
    ["idle", "Idle", "At rest, newest first"],
  ];
  let timer = null;
  let replying = ""; // id of the card whose reply box is open
  let draft = "";
  let busy = false;

  async function post(path, data) {
    try {
      const r = await fetch(path, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(data || {}) });
      const text = await r.text();
      let json = null;
      try { json = JSON.parse(text); } catch (_) {}
      return { ok: r.ok, data: json, error: r.ok ? "" : (text || r.statusText).trim() };
    } catch (_) {
      return { ok: false, data: null, error: "Cannot reach janetcode. Is it still running?" };
    }
  }

  function ago(iso) {
    const s = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 1000));
    if (!isFinite(s)) return "";
    if (s < 60) return "just now";
    if (s < 3600) return Math.floor(s / 60) + "m ago";
    if (s < 86400) return Math.floor(s / 3600) + "h ago";
    return Math.floor(s / 86400) + "d ago";
  }

  function card(r) {
    const kind = r.kind === "live" ? "this window" : r.kind === "bg" ? "background" : "stored";
    const peek = pend
      ? `<p class="dash-wait" data-testid="dash-pending">${esc(pend.text)}${pend.done ? " (the run finished; open it to continue)" : ""}</p>${pend.choices && pend.choices.length ? `<p class="dash-choices">${pend.choices.map((c) => `<code>${esc(c)}</code>`).join(" ")}</p>` : ""}`
      : r.waiting
      ? `<p class="dash-wait">${esc(r.waiting)}</p>`
      : (r.last ? `<p class="dash-peek" title="Last message">${esc(r.last)}</p>` : `<p class="dash-peek dash-none">No reply yet</p>`);
    const pend = r.pending;
    // A run's pending question has its reply box open already; the choices
    // fill it in. A finished run's question can only be read.
    const replyOpen = replying === r.id || (pend && !pend.done && r.canReply);
    const actions = [];
    if (r.canReply) actions.push(`<button type="button" data-act="reply" data-id="${esc(r.id)}">Reply</button>`);
    if (r.canStop) actions.push(`<button type="button" data-act="stop" data-id="${esc(r.id)}" class="danger">Stop</button>`);
    actions.push(`<button type="button" data-act="open" data-id="${esc(r.id)}">${r.current ? "Show" : "Open"}</button>`);
    actions.push(`<button type="button" data-act="pin" data-id="${esc(r.id)}" aria-pressed="${r.pinned ? "true" : "false"}" title="${r.pinned ? "Unpin" : "Pin"}">${r.pinned ? "Pinned" : "Pin"}</button>`);
    return `<li class="dash-card${r.current ? " cur" : ""}" data-id="${esc(r.id)}">
      <div class="dash-head"><span class="dash-title">${esc(r.title)}</span><span class="dash-kind">${esc(kind)}</span></div>
      ${peek}
      <div class="dash-meta">${esc(ago(r.updated))}</div>
      <div class="dash-actions">${actions.join("")}</div>
      ${replyOpen ? `<form class="dash-reply" data-testid="dash-pending-reply" data-auto="${replying === r.id ? "" : "1"}" data-id="${esc(r.id)}"><input type="text" aria-label="Reply to ${esc(r.title)}" placeholder="${r.waiting ? "Your answer" : "Message"}" value="${esc(draft)}"><button type="submit">Send</button></form>` : ""}
    </li>`;
  }

  function paint(data) {
    const g = data.groups || {};
    $("dash-dispatch-form").hidden = !data.dispatch;
    body.innerHTML = GROUPS.map(([key, label, hint]) => {
      const rows = g[key] || [];
      // A quiet inbox takes no room.
      return `<section class="dash-col" aria-label="${label}" data-group="${key}"${key === "awaiting" && !rows.length ? " hidden" : ""}>
        <h3>${label} <span class="dash-count">${rows.length}</span></h3>
        <p class="dash-hint">${hint}</p>
        <ul>${rows.length ? rows.map(card).join("") : `<li class="dash-empty">Nothing here</li>`}</ul>
      </section>`;
    }).join("");
    const inp = body.querySelector(".dash-reply:not([data-auto='1']) input");
    if (inp) { inp.focus(); inp.setSelectionRange(inp.value.length, inp.value.length); }
  }

  async function load() {
    if (busy || root.hidden) return;
    if (document.activeElement && document.activeElement.closest && document.activeElement.closest(".dash-reply")) {
      draft = document.activeElement.value; // keep what is being typed across a poll
      return;
    }
    try {
      const r = await fetch("/api/dashboard");
      if (!r.ok) throw new Error(await r.text());
      paint(await r.json());
      note.textContent = "";
    } catch (e) {
      note.textContent = "Could not load the dashboard: " + String(e.message || e).slice(0, 120);
    }
  }

  function open() {
    root.hidden = false;
    $("dash-btn") && $("dash-btn").setAttribute("aria-expanded", "true");
    load();
    clearInterval(timer);
    timer = setInterval(load, 3000);
    const first = root.querySelector("textarea, input, button");
    if (first) first.focus();
  }
  function close() {
    root.hidden = true;
    clearInterval(timer);
    $("dash-btn") && $("dash-btn").setAttribute("aria-expanded", "false");
  }

  body.addEventListener("click", async (ev) => {
    const b = ev.target.closest("button[data-act]");
    if (!b) return;
    const id = b.dataset.id;
    switch (b.dataset.act) {
      case "reply":
        replying = replying === id ? "" : id;
        draft = "";
        load();
        break;
      case "stop": {
        const r = await post("/api/dashboard/stop", { id });
        note.textContent = r.ok ? "Stop sent." : r.error;
        load();
        break;
      }
      case "pin": {
        const r = await post("/api/session/pin", { id, pinned: b.getAttribute("aria-pressed") !== "true" });
        if (!r.ok) note.textContent = r.error;
        window.dispatchEvent(new Event("janet:sessions-refresh"));
        load();
        break;
      }
      case "open": {
        const r = await post("/api/load", { id });
        if (!r.ok) { note.textContent = r.error; break; }
        close();
        location.reload();
        break;
      }
    }
  });
  body.addEventListener("submit", async (ev) => {
    const f = ev.target.closest("form.dash-reply");
    if (!f) return;
    ev.preventDefault();
    const text = f.querySelector("input").value.trim();
    if (!text) return;
    busy = true;
    const r = await post("/api/dashboard/reply", { id: f.dataset.id, text });
    busy = false;
    note.textContent = r.ok ? "Sent." : r.error;
    if (r.ok) { replying = ""; draft = ""; }
    load();
  });
  $("dash-dispatch-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const ta = $("dash-prompt");
    const prompt = ta.value.trim();
    if (!prompt) return;
    const r = await post("/api/dashboard/dispatch", { prompt });
    if (r.ok) { ta.value = ""; note.textContent = "Started background run " + ((r.data && r.data.id) || ""); }
    else note.textContent = r.error;
    load();
  });
  $("dash-close").addEventListener("click", close);
  root.addEventListener("keydown", (ev) => { if (ev.key === "Escape") { close(); const b = $("dash-btn"); if (b) b.focus(); } });
  const btn = $("dash-btn");
  if (btn) btn.addEventListener("click", () => (root.hidden ? open() : close()));
  window.janetDashboard = { open, close, paint };
})();
