// /connect: provider, base URL, key, model. "List models" asks the
// provider with the key as typed, so a rejected key shows up here, not
// on the first prompt.
(function (global) {
  "use strict";
  const { esc } = global.JanetRender;

  // JanetConnect wires the dialog in index.html and returns open, which
  // shows it with an optional note. closePalette, composer and status are
  // the page's: opening closes the palette and closing restores the
  // invoker through the shared modal handling.
  global.JanetConnect = function ({ closePalette, composer, status }) {
    const cx = {
      root: document.getElementById("connect"),
      form: document.getElementById("connect-form"),
      provider: document.getElementById("cx-provider"),
      custom: document.getElementById("cx-custom"),
      name: document.getElementById("cx-name"),
      type: document.getElementById("cx-type"),
      url: document.getElementById("cx-url"),
      keyRow: document.getElementById("cx-key-row"),
      key: document.getElementById("cx-key"),
      model: document.getElementById("cx-model"),
      models: document.getElementById("cx-models"),
      msg: document.getElementById("cx-msg"),
      list: document.getElementById("cx-list"),
      test: document.getElementById("cx-test"),
      envOn: document.getElementById("cx-env-on"),
      envField: document.getElementById("cx-env-field"),
      env: document.getElementById("cx-env"),
      makeDefault: document.getElementById("cx-default"),
      go: document.getElementById("cx-go"),
      providers: [],
      busy: false,
      gen: 0,
    };
    function cxSay(text, kind) {
      cx.msg.className = "cx-msg" + (kind ? " " + kind : "");
      cx.msg.textContent = text || "";
    }
    function cxExplain(x) {
      if (!x) return "";
      // Headline, then the provider's words, then what to do: the same
      // three parts the error card shows.
      // Each part as a sentence: the server's reasons are Go errors,
      // lowercase with no full stop ("this provider needs an API key").
      const sentence = (t) => { t = String(t).trim(); t = t.charAt(0).toUpperCase() + t.slice(1); return /[.!?]$/.test(t) ? t : t + "."; };
      return [x.headline, x.reason, x.next].filter(Boolean).map(sentence).join(" ");
    }
    function cxChosen() {
      const v = cx.provider.value;
      if (v === "custom-openai" || v === "custom-anthropic") {
        return { name: cx.name.value.trim(), type: v.slice(7), custom: true, has_key: false, needs_key: true };
      }
      return cx.providers.find((p) => p.name === v) || { name: v };
    }
    function cxPaint() {
      const p = cxChosen();
      cx.custom.hidden = !p.custom;
      if (!p.custom) cx.url.value = p.url || "";
      cx.envOn.checked = !!p.key_env;
      cx.env.value = p.key_env || "";
      cx.envField.hidden = !cx.envOn.checked;
      cx.keyRow.hidden = cx.envOn.checked;
      cx.url.placeholder = p.type === "anthropic" ? "default (api.anthropic.com)" : "https://…/v1";
      cx.key.placeholder = p.has_key ? "saved · leave empty to keep it" : (p.needs_key === false ? "not needed for a local server" : "Paste your API key");
      cx.models.innerHTML = "";
      cx.model.value = "";
      cx.model.placeholder = "List models, or type a model id";
      cxSay("");
    }
    function cxBody(action) {
      const p = cxChosen();
      return { action, name: p.name, type: p.custom ? p.type : "", url: cx.url.value.trim(), keys: cx.envOn.checked ? "" : cx.key.value, key_env: cx.envOn.checked ? cx.env.value.trim() : "", make_default: cx.makeDefault.checked, model: cx.model.value.trim() };
    }
    async function cxCall(action) {
      const r = await fetch("/api/connect", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(cxBody(action)) });
      if (!r.ok) return { error: { reason: (await r.text()) || r.statusText } };
      return r.json();
    }
    function cxBusy(on, label) {
      cx.busy = on;
      cx.list.disabled = cx.go.disabled = cx.test.disabled = on;
      cx.root.setAttribute("aria-busy", on ? "true" : "false");
      if (on) cxSay(label);
    }
    async function cxListModels() {
      if (cx.busy) return;
      cxBusy(true, "Asking the provider which models it serves…");
      let out;
      try { out = await cxCall("models"); } catch (e) { out = { error: { reason: String(e) } }; }
      cxBusy(false);
      cx.key.removeAttribute("aria-invalid");
      if (out.error) {
        if (out.error.headline === "Authentication failed") cx.key.setAttribute("aria-invalid", "true");
        cxSay(cxExplain(out.error), "bad");
        return;
      }
      const ms = out.models || [];
      const provider = cxChosen().name;
      cx.models.innerHTML = ms.map((m) => `<option value="${esc(m.id)}">${esc([provider, m.name, m.context ? Math.round(m.context / 1000) + "k context" : ""].filter(Boolean).join(" · "))}</option>`).join("");
      cxSay(ms.length ? `${ms.length} ${ms.length === 1 ? "model" : "models"} available. Pick one, then Connect.` : "The provider lists no models; type the model id.", "ok");
      cx.model.focus();
    }
    // Test connection reaches the provider with the key as typed and saves
    // nothing; the model list it finds is offered like List models does.
    async function cxTest() {
      if (cx.busy) return;
      cxBusy(true, "Testing the connection…");
      let out;
      try { out = await cxCall("test"); } catch (e) { out = { error: { reason: String(e) } }; }
      cxBusy(false);
      cx.key.removeAttribute("aria-invalid");
      if (out.error) {
        if (out.error.headline === "Authentication failed") cx.key.setAttribute("aria-invalid", "true");
        cxSay(cxExplain(out.error), "bad");
        return;
      }
      cxSay((out.note || "Connection works") + ". Nothing was saved.", "ok");
    }
    async function openConnect(note) {
      closePalette();
      // Opening again while open would reset what is half filled in.
      if (!cx.root.hidden && cx.root.dataset.ready) { if (note) cxSay(note); return; }
      delete cx.root.dataset.ready;
      const gen = ++cx.gen;
      cx.root.hidden = false;
      cxSay(note || "");
      let data = null;
      try { const r = await fetch("/api/connect"); if (r.ok) data = await r.json(); } catch (_) {}
      if (gen !== cx.gen || cx.root.hidden) return;
      if (!data) { cxSay("Connecting is not available in this session.", "bad"); return; }
      cx.providers = data.providers || [];
      if (data.config) document.getElementById("cx-path").textContent = data.config;
      const badge = (p) => (p.connected ? " · Connected" : p.key_env ? " · key from $" + p.key_env : p.has_key ? " · key saved" : "");
      const opt = (p) => `<option value="${esc(p.name)}">${esc(p.label + badge(p))}</option>`;
      cx.provider.innerHTML = cx.providers.map(opt).join("") +
        `<option value="custom-openai">Custom · OpenAI-compatible</option><option value="custom-anthropic">Custom · Anthropic-compatible</option>`;
      if (data.current && cx.providers.some((p) => p.name === data.current)) cx.provider.value = data.current;
      cxPaint();
      if (note) cxSay(note);
      cx.root.dataset.ready = "1";
      cx.provider.focus();
    }
    function closeConnect() {
      if (cx.root.hidden) return;
      cx.gen++;
      cx.root.hidden = true;
      cx.key.value = "";
      composer.focus();
    }
    cx.provider.addEventListener("change", cxPaint);
    cx.list.addEventListener("click", cxListModels);
    cx.test.addEventListener("click", cxTest);
    cx.envOn.addEventListener("change", () => {
      cx.envField.hidden = !cx.envOn.checked;
      cx.keyRow.hidden = cx.envOn.checked;
      if (cx.envOn.checked) cx.env.focus(); else cx.key.focus();
    });
    document.getElementById("cx-cancel").addEventListener("click", closeConnect);
    cx.root.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); closeConnect(); return; }
      // Tab stays inside the form while it is open.
      if (ev.key === "Tab") {
        const f = [...cx.root.querySelectorAll("select, input, button")].filter((el) => !el.disabled && el.offsetParent !== null);
        if (!f.length) return;
        const first = f[0], last = f[f.length - 1];
        if (ev.shiftKey && document.activeElement === first) { ev.preventDefault(); last.focus(); }
        else if (!ev.shiftKey && document.activeElement === last) { ev.preventDefault(); first.focus(); }
      }
    });
    cx.form.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      if (cx.busy) return;
      if (!cx.model.value.trim()) {
        // Enter with no model yet means "show me the models".
        await cxListModels();
        return;
      }
      cxBusy(true, "Connecting…");
      let out;
      try { out = await cxCall("connect"); } catch (e) { out = { error: { reason: String(e) } }; }
      cxBusy(false);
      if (out.error) { cxSay(cxExplain(out.error), "bad"); return; }
      closeConnect();
      status.textContent = out.note || "Connected";
    });

    return { open: openConnect };
  };
})(window);
