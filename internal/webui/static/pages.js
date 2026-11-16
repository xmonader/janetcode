// Pages: HTML the model attached to a turn with render_html, shown in a
// sandboxed frame above the written reply.
//
// The frame is sandbox="allow-scripts allow-popups
// allow-popups-to-escape-sandbox" and never allow-same-origin: its origin
// is opaque, so scripts inside it cannot read this document, the cookie,
// the token or the API. Popups may escape the sandbox so a clicked link
// opens as an ordinary new tab. The server adds the same sandbox as a CSP
// directive, so the URL is safe to open on its own as well.
(function () {
  "use strict";
  const MIN_H = 160, MAX_H = 640, START_H = 320;
  const frames = new Set();

  const themeNow = () => (document.documentElement.dataset.theme === "dark" ? "dark" : "light");
  const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  // build fills turn node n for page entry e. A page is drawn once: a
  // later update of the same entry must not reload the frame.
  function build(n, e, icon) {
    if (n.querySelector(".page-frame")) return;
    const d = e.data || {};
    n.className = "turn page-card";
    const url = "/page/" + encodeURIComponent(d.sid || "") + "/" + encodeURIComponent(d.n) + "?theme=" + themeNow();
    n.innerHTML =
      `<div class="page-bar"><span class="page-title">${esc(d.title || e.name || "Page")}</span>` +
      `<button type="button" class="act page-full" aria-label="Expand page to full screen" title="Expand">${icon("maximize")}</button></div>` +
      `<iframe class="page-frame" title="${esc(d.title || "Page")}" sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox" ` +
      `referrerpolicy="no-referrer" src="${url}" style="height:${START_H}px"></iframe>`;
    const frame = n.querySelector("iframe");
    const btn = n.querySelector(".page-full");
    frames.add(frame);
    const setFull = (on) => {
      n.classList.toggle("full", on);
      document.body.classList.toggle("page-full-open", on);
      btn.innerHTML = icon(on ? "minimize" : "maximize");
      btn.setAttribute("aria-label", on ? "Leave full screen" : "Expand page to full screen");
      btn.title = on ? "Close" : "Expand";
      if (on) btn.focus();
    };
    btn.addEventListener("click", () => setFull(!n.classList.contains("full")));
    n.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape" && n.classList.contains("full")) { ev.stopPropagation(); setFull(false); btn.focus(); }
    });
  }

  // Height reports come from the page itself; only the frame that sent
  // one may resize its own card.
  window.addEventListener("message", (ev) => {
    if (!ev.data || ev.data.type !== "janetcode-height") return;
    for (const f of frames) {
      if (f.contentWindow !== ev.source) continue;
      if (f.closest(".page-card.full")) return;
      const h = Math.max(MIN_H, Math.min(MAX_H, Math.ceil(Number(ev.data.h) || 0)));
      f.style.height = h + "px";
    }
  });

  // setTheme tells every open page the theme changed.
  function setTheme(theme) {
    for (const f of frames) {
      if (!f.isConnected) { frames.delete(f); continue; }
      try { f.contentWindow.postMessage({ type: "janetcode-theme", theme }, "*"); } catch (_) { /* frame gone */ }
    }
  }

  window.JanetPages = { build, setTheme };
})();
