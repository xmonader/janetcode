// The sessions rail: the live conversation, History grouped by day, pins,
// the content-searching filter, and click-to-resume. Reads S.firstUserText
// (names a conversation the server has not titled yet) and writes
// S.localNote (the status line's local message). app.js passes the page
// helpers it shares; loadSessions is returned for the places that change
// the session list (boot, new chat, a turn finishing).
(function (global) {
  "use strict";
  global.JanetSessionsRail = function ({ S, esc, icon, post, cleanTitle, clipLine, statusEl, input, shell, setCollapsed, refreshWorkspace }) {
    function relTime(iso) {
      const t = Date.parse((iso || "").replace(" ", "T"));
      if (!t) return "";
      const s = Math.max(0, (Date.now() - t) / 1000);
      if (s < 60) return "just now";
      if (s < 3600) return Math.floor(s / 60) + "m ago";
      if (s < 86400) return Math.floor(s / 3600) + "h ago";
      return Math.floor(s / 86400) + "d ago";
    }

    function dayBucket(iso) {
      const t = Date.parse((iso || "").replace(" ", "T"));
      if (!t) return "Older";
      const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
      const diff = Math.round((day(new Date()) - day(new Date(t))) / 86400000);
      if (diff <= 0) return "Today";
      if (diff === 1) return "Yesterday";
      if (diff < 7) return diff + " days ago";
      return "Older";
    }

    // renderSessions paints the rail: the live conversation up top, then
    // the directory's past conversations under History, grouped by day,
    // filtered by the rail's search field.
    // Pinned conversations ride at the top of the rail, kept in this browser.
    // Pins live in the session files (row.starred); sessions_ui.js moves the
    // old browser-local ones across once.
    let pinnedIds = new Set();
    function pinnedSet() { return pinnedIds; }
    async function togglePin(id) {
      await post("/api/session/pin", { id, pinned: !pinnedIds.has(id) });
      loadSessions();
    }
    window.addEventListener("janet:sessions-refresh", () => loadSessions());
    function renderSessions(rows, filter) {
      const live = rows.find((r) => r.current);
      const curEl = document.getElementById("current-session");
      if (live) {
        let label = cleanTitle(live.label || "");
        if (!label || label.startsWith("[") || label.startsWith("<")) label = clipLine(S.firstUserText) || "New conversation";
        curEl.innerHTML =
          `<div class="cur-row" title="The conversation open now">` +
          `<span class="cur-dot"></span><span class="cur-label">${esc(live.prefix || "")}${esc(label)}</span>` +
          `<span class="cur-live">open</span></div>`;
        curEl.hidden = false;
      } else {
        curEl.hidden = true;
      }
      const ul = document.getElementById("sessions");
      const q = (filter || "").trim().toLowerCase();
      const match = (r) =>
        !q ||
        (r.label || "").toLowerCase().includes(q) ||
        (r.id || "").toLowerCase().includes(q) ||
        (r.note || "").toLowerCase().includes(q) ||
        !!r.hit;
      pinnedIds = new Set(rows.filter((r) => r.starred).map((r) => r.id));
      const ui = window.sessionsUI;
      if (ui) ui.seen(rows);
      const archivedTab = ui && ui.tab() === "archived";
      const kept = rows.filter((r) => !r.current).filter((r) => !!r.archived === archivedTab).filter(match).slice(0, 40);
      if (!kept.length) {
        ul.innerHTML = `<li class="quiet">${q ? "No conversation matches" : archivedTab ? "Archived conversations will appear here." : "Earlier conversations in this project will appear here."}</li>`;
        return;
      }
      const pins = pinnedSet();
      const seen = new Set();
      const parts = [];
      const ordered = kept.filter((r) => pins.has(r.id)).concat(kept.filter((r) => !pins.has(r.id)));
      for (const r of ordered) {
        const isPin = pins.has(r.id);
        const b = isPin ? "Pinned" : dayBucket(r.updated);
        if (!seen.has(b)) {
          seen.add(b);
          parts.push(`<li class="sess-group">${esc(b)}</li>`);
        }
        const pinBtn = `<button type="button" class="pin${isPin ? " on" : ""}" data-pin="${esc(r.id)}" title="${isPin ? "Unpin" : "Pin to the top"}" aria-label="${isPin ? "Unpin" : "Pin"} conversation">${icon("pin")}</button>`;
        parts.push(
          `<li class="${r.current ? "on" : ""}${isPin ? " pinned" : ""}" data-id="${esc(r.id)}" title="${esc(r.note || "Resume this conversation")}" style="padding-left:${8 + (r.depth || 0) * 10}px"><span>${esc(r.prefix || "")}${esc(r.label)}</span><span class="when">${esc(relTime(r.updated))}${r.note ? " · parent elsewhere" : ""}</span>${ui ? ui.rowExtras(r) : ""}${pinBtn}${r.hit ? `<span class="sess-hit">${r.turn ? "turn " + r.turn + ": " : ""}${esc(r.hit)}</span>` : ""}</li>`
        );
      }
      ul.innerHTML = parts.join("");
    }

    // The filter searches what the conversations said as well as their
    // titles: with three letters or more the server reads the sessions for
    // them, so that request waits for a pause in the typing.
    async function loadSessions() {
      try {
        const q = sessionFilter.value.trim();
        const url = q.length >= 3 ? "/api/sessions?q=" + encodeURIComponent(q) : "/api/sessions";
        const rows = await (await fetch(url)).json();
        renderSessions(rows, sessionFilter.value);
      } catch (_) {}
    }

    const sessionFilter = document.getElementById("session-filter");
    let sessionFilterTimer = 0;
    sessionFilter.addEventListener("input", () => {
      clearTimeout(sessionFilterTimer);
      sessionFilterTimer = setTimeout(loadSessions, 250);
    });

    document.getElementById("sessions").addEventListener("click", async (ev) => {
      if (window.sessionsUI && window.sessionsUI.click(ev)) return;
      const pin = ev.target.closest("[data-pin]");
      if (pin) { ev.stopPropagation(); togglePin(pin.dataset.pin); return; }
      const li = ev.target.closest("li[data-id]");
      if (!li || li.classList.contains("on")) return;
      // The server clears the page itself: it sends "reset" and then the
      // loaded thread on /events before it answers here. Clearing the log
      // again on this answer wiped a thread that had already arrived,
      // leaving the empty page over a conversation that was loaded.
      // A session another janetcode is running turns in is refused with a
      // conflict that names the way on: offer it, and continue in a copy.
      let opened = false;
      try {
        const r = await fetch("/api/load", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ id: li.dataset.id }) });
        const why = r.ok ? "" : await r.text();
        if (r.ok) opened = true;
        else if (r.status === 409 && why.includes("fork") && window.confirm(why.trim() + "\n\nContinue in a copy of it?")) {
          opened = await post("/api/load", { id: li.dataset.id, fork: true });
        } else statusEl.textContent = why || r.statusText;
      } catch (e) {
        statusEl.textContent = S.localNote = "Cannot reach janetcode. Is it still running?";
      }
      if (opened) {
        if (window.matchMedia("(max-width: 960px)").matches && !shell.classList.contains("nav-collapsed")) {
          setCollapsed("nav-collapsed", true);
        }
        refreshWorkspace();
        loadSessions();
        input.focus();
      }
    });
    return { loadSessions };
  };
})(window);
