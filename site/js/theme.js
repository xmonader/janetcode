// Theme toggle: persists an explicit light/dark choice in localStorage,
// on top of the prefers-color-scheme default. Vanilla JS, no dependencies,
// no network access. Fails silently if storage is unavailable (e.g. a
// locked-down file:// context), since the page works without it.
(function () {
  "use strict";

  var KEY = "janetcode-theme";

  function stored() {
    try {
      return localStorage.getItem(KEY);
    } catch (e) {
      return null;
    }
  }

  function systemPrefersLight() {
    return (
      window.matchMedia &&
      window.matchMedia("(prefers-color-scheme: light)").matches
    );
  }

  function currentTheme() {
    var t = stored();
    if (t === "light" || t === "dark") return t;
    return systemPrefersLight() ? "light" : "dark";
  }

  function apply(theme, save) {
    document.documentElement.setAttribute("data-theme", theme);
    if (save) {
      try {
        localStorage.setItem(KEY, theme);
      } catch (e) {
        /* ignore */
      }
    }
    updateButton(theme);
  }

  function updateButton(theme) {
    var btn = document.getElementById("theme-toggle");
    if (!btn) return;
    var next = theme === "dark" ? "light" : "dark";
    btn.textContent = theme === "dark" ? "☾" : "☀";
    btn.setAttribute("aria-label", "Switch to " + next + " theme");
    btn.setAttribute("title", "Switch to " + next + " theme");
  }

  function init() {
    updateButton(currentTheme());
    var btn = document.getElementById("theme-toggle");
    if (!btn) return;
    btn.addEventListener("click", function () {
      var next = currentTheme() === "dark" ? "light" : "dark";
      apply(next, true);
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();

// The features page's table of contents is thirty entries long: on a phone
// that is a screenful of links before the first card, so it starts closed
// there. Wider screens keep it open, where it costs nothing.
(function () {
  var toc = document.querySelector('.toc-box');
  if (toc && window.matchMedia('(max-width: 639px)').matches) toc.open = false;
})();
