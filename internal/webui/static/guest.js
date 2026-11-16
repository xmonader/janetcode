// Guest links: the bar for an active link, the guest's Requests the owner
// sends as turns or dismisses, copy and stop. Self-contained: the guest
// state arrives in the hello/state payload and the "guest" stream event, and
// app.js calls apply() with it. share() is the palette's "Share with guest".
(function (global) {
  "use strict";
  global.JanetGuest = function ({ post, statusEl }) {
    // Guest links: the bar shows an active link and the messages a guest
    // left, each a Request the owner sends as a turn or dismisses. The text
    // is guest-written, so it only ever goes in with textContent.
    const guestBar = document.getElementById("guest-bar");
    const guestReqs = document.getElementById("guest-reqs");
    const shareBtn = document.getElementById("share-btn");
    let guestPath = "";
    function applyGuest(g) {
      g = g || {};
      const reqs = g.requests || [];
      guestPath = g.active ? g.path || "" : "";
      guestBar.hidden = !g.active && reqs.length === 0;
      shareBtn.setAttribute("aria-pressed", g.active ? "true" : "false");
      shareBtn.classList.toggle("on", !!g.active);
      const state = document.getElementById("guest-state");
      state.textContent = g.active
        ? "Sharing with a guest · read-only · ends " + new Date(g.expires).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })
        : "Link ended";
      document.getElementById("guest-copy").hidden = !g.active;
      guestReqs.replaceChildren(...reqs.map((r) => {
        const li = document.createElement("li");
        const tag = document.createElement("span");
        tag.className = "k";
        tag.textContent = "Request";
        const txt = document.createElement("span");
        txt.className = "guest-text";
        txt.textContent = r.text;
        const mk = (label, action, cls) => {
          const b = document.createElement("button");
          b.type = "button";
          b.className = cls;
          b.textContent = label;
          b.addEventListener("click", () => post("/api/guest", { action, id: r.id }));
          return b;
        };
        li.append(tag, txt, mk("Send", "send", "act primary"), mk("Dismiss", "dismiss", "act"));
        return li;
      }));
    }
    async function copyGuestLink() {
      if (!guestPath) return;
      const url = location.origin + guestPath;
      try {
        await navigator.clipboard.writeText(url);
        statusEl.textContent = "Guest link copied. It is read-only and ends in 24 hours or when you stop sharing.";
      } catch (_) {
        statusEl.textContent = "Guest link: " + url;
      }
    }
    async function shareWithGuest() {
      if (!guestPath) {
        const r = await fetch("/api/guest", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ action: "create" }) }).catch(() => null);
        if (!r || !r.ok) { statusEl.textContent = r ? (await r.text()).trim() : "Cannot reach janetcode."; return; }
        const out = await r.json();
        guestPath = new URL(out.url).pathname;
      }
      await copyGuestLink();
    }
    shareBtn.addEventListener("click", shareWithGuest);
    document.getElementById("guest-copy").addEventListener("click", copyGuestLink);
    document.getElementById("guest-stop").addEventListener("click", () => post("/api/guest", { action: "stop" }));
    return { apply: applyGuest, share: shareWithGuest };
  };
})(window);
