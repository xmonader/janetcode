// The workbench shell: the two collapsible rails (sessions, workbench) and
// their tab strip, the web-preview drawer, and drag-to-resize for the three
// panels. State is the layout itself (classes on .shell, widths in CSS vars,
// localStorage); nothing here reads the conversation. app.js passes .shell
// and uses the returned entry points: palette actions click the toggles,
// link cards call pvOpen/pvLoad, the rail closes itself with setCollapsed.
(function (global) {
  "use strict";
  global.JanetPanels = function ({ shell }) {
    const navToggle = document.getElementById("nav-toggle");
    const workToggle = document.getElementById("work-toggle");
    const narrow = window.matchMedia("(max-width: 960px)");
    function setCollapsed(cls, on) {
      shell.classList.toggle(cls, on);
      // On a narrow screen both rails are transient overlays: opening one
      // closes the other rather than stacking two drawers, and neither
      // says anything about the desktop layout.
      if (narrow.matches) {
        if (!on) shell.classList.add(cls === "nav-collapsed" ? "work-collapsed" : "nav-collapsed");
        return;
      }
      localStorage.setItem("janetcode-" + cls, on ? "1" : "");
    }
    shell.addEventListener("click", (ev) => {
      if (ev.target === shell && narrow.matches) {
        shell.classList.add("nav-collapsed");
        shell.classList.add("work-collapsed");
      }
    });
    narrow.addEventListener("change", () => {
      shell.classList.toggle("nav-collapsed", narrow.matches || localStorage.getItem("janetcode-nav-collapsed") === "1");
      shell.classList.toggle("work-collapsed", narrow.matches || localStorage.getItem("janetcode-work-collapsed") !== "");
    });
    navToggle.addEventListener("click", () => setCollapsed("nav-collapsed", !shell.classList.contains("nav-collapsed")));
    workToggle.addEventListener("click", () => setCollapsed("work-collapsed", !shell.classList.contains("work-collapsed")));
    // The work rail starts closed. The session rail starts open, and only
    // stays shut when this browser was told to shut it.
    if (localStorage.getItem("janetcode-nav-collapsed") === "1" || narrow.matches) shell.classList.add("nav-collapsed");
    // A panel left open on a desktop does not reopen over a phone's thread.
    if (localStorage.getItem("janetcode-work-collapsed") !== "" || narrow.matches) shell.classList.add("work-collapsed");
    // openWorkTab opens the workbench on a tab, from the palette or a link.
    function openWorkTab(pane) {
      if (shell.classList.contains("work-collapsed")) setCollapsed("work-collapsed", false);
      const btn = document.querySelector('.work-tabs [data-pane="' + pane + '"]');
      if (btn) btn.click();
    }
    document.querySelector(".work-tabs").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-pane]");
      if (!btn) return;
      const pane = btn.dataset.pane;
      document.querySelectorAll(".work-tabs [data-pane]").forEach((b) => b.classList.toggle("on", b === btn));
      document.querySelectorAll(".work-pane").forEach((p) => { p.hidden = p.dataset.pane !== pane; });
    });

    const pvPane = document.getElementById("preview");
    const pvToggle = document.getElementById("preview-toggle");
    const pvUrl = document.getElementById("pv-url");
    const pvFrame = document.getElementById("pv-frame");
    const pvEmpty = document.getElementById("pv-empty");
    const pvStage = document.getElementById("pv-stage");

    function pvLoad(url) {
      url = String(url || "").trim();
      if (!url) return;
      if (!/^https?:\/\//i.test(url)) url = "http://" + url;
      pvUrl.value = url;
      pvFrame.src = url;
      pvFrame.hidden = false;
      pvEmpty.hidden = true;
      localStorage.setItem("janetcode-preview-url", url);
    }
    function pvOpen(open) {
      // The drawer slides on .shell.pv-open; hiding it with [hidden] would
      // kill the transition, so the class is the whole story.
      shell.classList.toggle("pv-open", open);
      pvToggle.classList.toggle("on", open);
      localStorage.setItem("janetcode-preview-open", open ? "1" : "");
      if (open && pvFrame.hidden && pvUrl.value) pvLoad(pvUrl.value);
    }
    pvToggle.addEventListener("click", () => pvOpen(!shell.classList.contains("pv-open")));
    document.getElementById("pv-close").addEventListener("click", () => pvOpen(false));
    pvUrl.addEventListener("keydown", (ev) => { if (ev.key === "Enter") { ev.preventDefault(); pvLoad(pvUrl.value); } });
    document.getElementById("pv-refresh").addEventListener("click", () => { if (pvUrl.value) pvLoad(pvUrl.value); });
    document.getElementById("pv-ext").addEventListener("click", () => { if (pvUrl.value) window.open(pvUrl.value, "_blank", "noopener"); });
    const pvWidthBtn = document.getElementById("pv-width");
    pvWidthBtn.addEventListener("click", () => {
      const narrow = pvStage.classList.toggle("narrow");
      pvWidthBtn.classList.toggle("on", narrow);
      localStorage.setItem("janetcode-preview-narrow", narrow ? "1" : "");
    });

    // Drag-to-resize for the three panels: the nav rail widens as the
    // pointer moves right (dir 1), the right-edge drawers widen as it
    // moves left (dir -1). Widths persist per browser; double-clicking a
    // handle resets that panel to its default.
    function dragResize(handle, dir, getW, setW, min, max, storageKey) {
      if (!handle) return;
      handle.addEventListener("pointerdown", (ev) => {
        if (ev.button !== 0) return;
        ev.preventDefault();
        const startX = ev.clientX;
        const startW = getW();
        handle.setPointerCapture(ev.pointerId);
        document.body.classList.add("resizing");
        const move = (e) => {
          const w = Math.round(startW + (e.clientX - startX) * dir);
          setW(Math.max(min, Math.min(max, w)));
        };
        const up = () => {
          handle.removeEventListener("pointermove", move);
          handle.removeEventListener("pointerup", up);
          handle.removeEventListener("pointercancel", up);
          document.body.classList.remove("resizing");
          try { localStorage.setItem(storageKey, String(Math.round(getW()))); } catch (_) {}
        };
        handle.addEventListener("pointermove", move);
        handle.addEventListener("pointerup", up);
        handle.addEventListener("pointercancel", up);
      });
      handle.addEventListener("dblclick", () => {
        setW(0);
        try { localStorage.removeItem(storageKey); } catch (_) {}
      });
    }
    const navPanel = document.querySelector(".nav");
    const workPanel = document.getElementById("work");
    // The open drawer yields the thread/composer width via these vars (see
    // .shell rules in app.css); they track any drag-resize.
    const setWorkVar = (w) => {
      workPanel.style.width = w ? w + "px" : "";
      if (w) shell.style.setProperty("--work-w-actual", w + "px");
      else shell.style.removeProperty("--work-w-actual");
    };
    const setPvVar = (w) => {
      pvPane.style.width = w ? w + "px" : "";
      if (w) shell.style.setProperty("--pv-w-actual", w + "px");
      else shell.style.removeProperty("--pv-w-actual");
    };
    dragResize(document.getElementById("nav-resize"), 1,
      () => navPanel.getBoundingClientRect().width,
      (w) => document.documentElement.style.setProperty("--nav-w", (w || 276) + "px"),
      200, 440, "janetcode-nav-w");
    dragResize(document.getElementById("work-resize"), -1,
      () => workPanel.getBoundingClientRect().width,
      setWorkVar,
      300, 520, "janetcode-work-w");
    dragResize(document.getElementById("pv-resize"), -1,
      () => pvPane.getBoundingClientRect().width,
      setPvVar,
      320, 960, "janetcode-preview-w");
    try {
      const savedNav = parseInt(localStorage.getItem("janetcode-nav-w") || "", 10);
      if (savedNav >= 200 && savedNav <= 440) document.documentElement.style.setProperty("--nav-w", savedNav + "px");
      const savedWork = parseInt(localStorage.getItem("janetcode-work-w") || "", 10);
      if (savedWork >= 300 && savedWork <= 520) setWorkVar(savedWork);
      const savedPv = parseInt(localStorage.getItem("janetcode-preview-w") || "", 10);
      if (savedPv >= 320 && savedPv <= 960) setPvVar(savedPv);
    } catch (_) {}
    pvUrl.value = localStorage.getItem("janetcode-preview-url") || "";
    if (localStorage.getItem("janetcode-preview-narrow")) { pvStage.classList.add("narrow"); pvWidthBtn.classList.add("on"); }
    if (localStorage.getItem("janetcode-preview-open")) pvOpen(true);
    return { navToggle, workToggle, pvToggle, pvOpen, pvLoad, setCollapsed, openWorkTab, narrow };
  };
})(window);
