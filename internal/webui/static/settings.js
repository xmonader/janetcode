// Appearance and sound settings: the theme switch (and the settings
// dialog's theme/font/density segments), the notification ding. State is the
// sound flag only; theme, font scale and density live on <html> datasets.
// app.js passes the icon helper and a getter for the terminal (built after
// this file runs), and reads back the few entry points it needs.
(function (global) {
  "use strict";
  global.JanetSettings = function ({ icon, terminal }) {
    const themeBtn = document.getElementById("theme-toggle");
    function drawTheme() {
      const dark = document.documentElement.dataset.theme === "dark";
      themeBtn.innerHTML = icon(dark ? "sun" : "moon");
      const label = dark ? "Switch to light theme" : "Switch to dark theme";
      themeBtn.setAttribute("aria-label", label);
      themeBtn.title = label;
    }
    function applyThemeChoice(choice) {
      localStorage.setItem("janetcode-theme", choice);
      document.documentElement.dataset.themeChoice = choice;
      const dark = choice === "dark" || (choice === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
      document.documentElement.dataset.theme = dark ? "dark" : "light";
      terminal().restyle();
      drawTheme();
      JanetPages.setTheme(dark ? "dark" : "light");
      document.querySelectorAll("#theme-seg [data-theme]").forEach((b) => {
        const on = b.dataset.theme === choice;
        b.classList.toggle("on", on);
        b.setAttribute("aria-pressed", on ? "true" : "false");
      });
    }
    themeBtn.addEventListener("click", () => {
      applyThemeChoice(document.documentElement.dataset.theme === "dark" ? "light" : "dark");
    });
    drawTheme();
    matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
      if (document.documentElement.dataset.themeChoice === "system") applyThemeChoice("system");
    });
    const settingsEl = document.getElementById("settings");
    let soundOn = localStorage.getItem("janetcode-sound") === "1";
    function paintSound() {
      const b = document.getElementById("sound-btn");
      if (b) b.setAttribute("aria-checked", soundOn ? "true" : "false");
    }
    const quiet = () => { try { return localStorage.getItem("janetcode-quiet") === "1"; } catch (_) { return false; } };
    function ding() {
      if (!soundOn || quiet()) return;
      try {
        const ctx = new AudioContext();
        const o = ctx.createOscillator();
        const g = ctx.createGain();
        o.frequency.value = 880;
        o.connect(g);
        g.connect(ctx.destination);
        g.gain.setValueAtTime(0.04, ctx.currentTime);
        g.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.12);
        o.start();
        o.stop(ctx.currentTime + 0.13);
      } catch (_) {}
    }
    function openSettings() {
      settingsEl.hidden = false;
      applyThemeChoice(document.documentElement.dataset.themeChoice || "system");
      setFontScale(document.documentElement.dataset.fontScale || "medium");
      paintSound();
      setDensity(document.documentElement.dataset.density || "comfortable");
    }
    document.getElementById("settings-btn").addEventListener("click", openSettings);
    document.getElementById("settings-close").addEventListener("click", () => { settingsEl.hidden = true; });
    document.getElementById("theme-seg").addEventListener("click", (ev) => {
      const b = ev.target.closest("[data-theme]");
      if (b) applyThemeChoice(b.dataset.theme);
    });
    function setFontScale(v) {
      document.documentElement.dataset.fontScale = v;
      try { localStorage.setItem("janetcode-font-scale", v); } catch (_) {}
      document.querySelectorAll("#font-seg [data-font]").forEach((x) => {
        const on = x.dataset.font === v;
        x.classList.toggle("on", on);
        x.setAttribute("aria-pressed", on ? "true" : "false");
      });
    }
    function setDensity(v) {
      document.documentElement.dataset.density = v;
      try { localStorage.setItem("janetcode-density", v); } catch (_) {}
      document.querySelectorAll("#density-seg [data-density]").forEach((x) => {
        const on = x.dataset.density === v;
        x.classList.toggle("on", on);
        x.setAttribute("aria-pressed", on ? "true" : "false");
      });
    }
    document.getElementById("font-seg").addEventListener("click", (ev) => {
      const b = ev.target.closest("[data-font]");
      if (b) setFontScale(b.dataset.font);
    });
    document.getElementById("density-seg").addEventListener("click", (ev) => {
      const b = ev.target.closest("[data-density]");
      if (b) setDensity(b.dataset.density);
    });
    document.getElementById("sound-btn").addEventListener("click", () => {
      soundOn = !soundOn;
      localStorage.setItem("janetcode-sound", soundOn ? "1" : "");
      paintSound();
      if (soundOn) ding();
    });
    paintSound();
    return { applyThemeChoice, openSettings, setFontScale, setDensity, ding, quiet, soundOn: () => soundOn, el: settingsEl };
  };
})(window);
