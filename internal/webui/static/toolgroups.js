// Tool activity grouping. Consecutive tool calls in the conversation sit in
// one collapsible card with a kind filter (reads, changes, searches,
// commands, other) and expand/collapse-all, so a long run of calls reads
// as one step of work instead of a wall of rows. app.js still renders
// every call as its own article.turn; this file only decides where that
// article lives and keeps the card's header in step with its members.
(function (global) {
  const CATS = [
    ["read", "Reads"],
    ["change", "Changes"],
    ["search", "Searches"],
    ["cmd", "Commands"],
    ["other", "Other"],
  ];

  // catOf sorts a tool entry into the filter's vocabulary.
  function catOf(e) {
    const n = String((e && e.name) || "").toLowerCase();
    if (/^(edit_file|edit|multi_edit|apply_patch|replace|write_file|write|create_file)$/.test(n)) return "change";
    if (/^(bash|shell|run|job|jobs)$/.test(n)) return "cmd";
    if (/grep|search|find|glob|references|definition|symbols|repo_map/.test(n)) return "search";
    if (/^(read_file|read|read_many|list_dir|ls|expand_output)$/.test(n)) return "read";
    return "other";
  }

  function JanetToolGroups({ log, entry, setOpen, isOpen, icon }) {
    function bodyOf(g) { return g.querySelector(".tg-body"); }

    function newGroup() {
      const g = document.createElement("section");
      g.className = "tool-group";
      g.dataset.filter = "all";
      g.innerHTML =
        `<header class="tg-head">` +
        `<button type="button" class="tg-toggle" aria-expanded="true" title="Fold these tool calls"><span class="chev">${icon("chevron-down")}</span><span class="tg-title"></span></button>` +
        `<span class="tg-filters" role="group" aria-label="Filter tool calls"></span>` +
        `<span class="tg-bulk"><button type="button" class="tg-expand" title="Open every call">Expand all</button><button type="button" class="tg-collapse" title="Close every call">Collapse all</button></span>` +
        `</header><div class="tg-body"></div>`;
      return g;
    }

    // place puts a freshly created entry node in the log: a tool joins the
    // card at the tail (or opens one), anything else goes to the end.
    function place(n, e) {
      if (e.kind !== "tool") { log.appendChild(n); return; }
      let g = log.lastElementChild;
      if (!g || !g.classList.contains("tool-group")) {
        g = newGroup();
        log.appendChild(g);
      }
      bodyOf(g).appendChild(n);
    }

    function groupOf(n) { return n && n.closest ? n.closest(".tool-group") : null; }

    // refresh redraws a card's header and re-applies its filter. Called
    // after each member renders, because a render resets the member's
    // class list.
    function refresh(n) {
      const g = groupOf(n);
      if (!g) return;
      const members = [...bodyOf(g).children];
      const counts = {};
      let running = 0, failed = 0;
      const filter = g.dataset.filter;
      for (const m of members) {
        const e = entry(m.dataset.id);
        if (!e) continue;
        const c = catOf(e);
        m.dataset.cat = c;
        counts[c] = (counts[c] || 0) + 1;
        if (e.running && !e.done) running++;
        if (e.error || e.denied) failed++;
        m.classList.toggle("tg-hide", filter !== "all" && c !== filter);
      }
      g.dataset.n = String(members.length);
      g.classList.toggle("is-running", running > 0);
      const title = g.querySelector(".tg-title");
      title.textContent = members.length + " tool call" + (members.length === 1 ? "" : "s") +
        (running ? " · " + running + " running" : "") + (failed ? " · " + failed + " declined or failed" : "");
      const cats = CATS.filter(([k]) => counts[k]);
      g.querySelector(".tg-filters").innerHTML = cats.length < 2 ? "" :
        `<button type="button" class="tg-f${filter === "all" ? " on" : ""}" data-f="all">All ${members.length}</button>` +
        cats.map(([k, label]) => `<button type="button" class="tg-f${filter === k ? " on" : ""}" data-f="${k}">${label} ${counts[k]}</button>`).join("");
    }

    // release is called when an entry node is removed: an emptied card goes
    // with its last member.
    function release(n) {
      const g = groupOf(n);
      if (!g) return;
      queueMicrotask(() => { if (!bodyOf(g).children.length) g.remove(); else refresh(bodyOf(g).firstElementChild); });
    }

    log.addEventListener("click", (ev) => {
      const g = ev.target.closest(".tool-group");
      if (!g) return;
      const t = ev.target.closest(".tg-toggle");
      if (t) {
        const folded = g.classList.toggle("folded");
        t.setAttribute("aria-expanded", String(!folded));
        t.querySelector(".chev").innerHTML = icon(folded ? "chevron-right" : "chevron-down");
        return;
      }
      const f = ev.target.closest(".tg-f");
      if (f) {
        g.dataset.filter = f.dataset.f;
        refresh(bodyOf(g).firstElementChild);
        return;
      }
      const all = ev.target.closest(".tg-expand, .tg-collapse");
      if (all) {
        const want = all.classList.contains("tg-expand");
        for (const m of bodyOf(g).children) {
          const e = entry(m.dataset.id);
          if (e && (e.preview || e.output) && isOpen(e) !== want) setOpen(e, want);
        }
      }
    });

    return { place, refresh, release, catOf };
  }

  global.JanetToolGroups = JanetToolGroups;
  global.JanetToolGroups.catOf = catOf;
})(window);
