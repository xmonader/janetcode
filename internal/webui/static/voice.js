// The page's language and its voice button. Both come from /api/ui once
// at load: the catalogue for the interface's language (the page's own
// chrome is translated by matching each element's English text, so the
// markup stays in one language), and whether the server can transcribe.
(function () {
  "use strict";
  let tr = null; // English text -> translation, or null for English

  const announce = (text) => {
    const el = document.getElementById("sr-announce");
    if (el) el.textContent = text;
  };
  const t = (s) => (tr && tr[s]) || s;

  // translateTree swaps the text of leaf elements and the placeholder,
  // title and aria-label attributes that exactly match a catalogue key.
  function translateTree(root) {
    if (!tr || !root || root.nodeType !== 1) return;
    const els = [root, ...root.querySelectorAll("*")];
    for (const el of els) {
      if (el.childElementCount === 0 && el.textContent) {
        const k = el.textContent.trim();
        if (tr[k] && el.textContent.trim() !== tr[k]) el.textContent = tr[k];
      }
      for (const a of ["placeholder", "title", "aria-label"]) {
        const v = el.getAttribute(a);
        if (v && tr[v]) el.setAttribute(a, tr[v]);
      }
    }
  }

  function wireVoice() {
    const btn = document.getElementById("voice-btn");
    const input = document.getElementById("input");
    if (!btn || !input || !window.MediaRecorder || !navigator.mediaDevices) return;
    btn.hidden = false;
    let rec = null;
    let chunks = [];
    const setState = (recording, busy) => {
      btn.setAttribute("aria-pressed", recording ? "true" : "false");
      btn.classList.toggle("on", recording);
      btn.disabled = !!busy;
      const label = busy ? t("Transcribing…") : recording ? t("Stop recording") : t("Record a voice prompt");
      btn.title = label;
      btn.setAttribute("aria-label", label);
    };
    btn.addEventListener("click", async () => {
      if (rec) {
        rec.stop();
        return;
      }
      let stream;
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      } catch (e) {
        announce("Microphone: " + (e && e.message ? e.message : e));
        return;
      }
      chunks = [];
      rec = new MediaRecorder(stream);
      rec.ondataavailable = (ev) => { if (ev.data && ev.data.size) chunks.push(ev.data); };
      rec.onstop = async () => {
        stream.getTracks().forEach((tr) => tr.stop());
        const type = rec.mimeType || "audio/webm";
        rec = null;
        setState(false, true);
        announce(t("Transcribing…"));
        const ext = type.includes("ogg") ? "ogg" : type.includes("mp4") ? "mp4" : "webm";
        const fd = new FormData();
        fd.append("audio", new Blob(chunks, { type }), "voice." + ext);
        try {
          const r = await fetch("/api/voice", { method: "POST", body: fd });
          if (!r.ok) throw new Error((await r.text()).trim() || r.status);
          const text = ((await r.json()).text || "").trim();
          if (text) {
            input.value = input.value.trim() ? input.value.replace(/\s*$/, " ") + text : text;
            input.dispatchEvent(new Event("input", { bubbles: true }));
            input.focus();
          }
          announce(text ? "Heard: " + text : "Nothing was heard");
        } catch (e) {
          announce("Voice: " + (e && e.message ? e.message : e));
        } finally {
          setState(false, false);
        }
      };
      rec.start();
      setState(true, false);
      announce(t("Stop recording"));
    });
    setState(false, false);
  }

  fetch("/api/ui")
    .then((r) => (r.ok ? r.json() : {}))
    .then((ui) => {
      if (ui.lang && ui.lang !== "en") {
        document.documentElement.lang = ui.lang;
        tr = ui.strings || null;
        translateTree(document.body);
        // Rows added later (menus, panes) carry the same English labels.
        new MutationObserver((ms) => {
          for (const m of ms) m.addedNodes.forEach((n) => translateTree(n));
        }).observe(document.body, { childList: true, subtree: true });
      }
      if (ui.voice) wireVoice();
    })
    .catch(() => {});
  window.janetI18n = { t };
})();
