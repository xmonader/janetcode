// Who controls the session. Several pages can be open on one server (a
// laptop and a phone); one of them controls it and the rest are refused
// when they try to change anything. This bar says so, and offers to take
// control at a turn boundary. Opening the page with ?observe=1 attaches it
// as an observer: it follows the session and can change nothing until it
// takes control. The control state arrives in the hello payload and the
// "control" stream event (docs/manual/protocol.md); app.js calls apply().
(function (global) {
  "use strict";
  global.JanetControl = function () {
    const bar = document.getElementById("control-bar");
    const text = document.getElementById("control-state");
    const takeBtn = document.getElementById("control-take");
    const observing = new URLSearchParams(location.search).has("observe");
    let snap = null;

    // Every page of one browser shares the cookie that names its client,
    // so an observer page names itself: its own id on every request.
    let me = "";
    if (observing) {
      try { me = sessionStorage.getItem("janetcode-observer-id") || ""; } catch (_) { /* private mode */ }
      if (!me) {
        me = "obs-" + Math.random().toString(36).slice(2, 12);
        try { sessionStorage.setItem("janetcode-observer-id", me); } catch (_) { /* private mode */ }
      }
      const plainFetch = global.fetch.bind(global);
      global.fetch = (input, init) => {
        const url = typeof input === "string" ? input : input && input.url;
        if (typeof url === "string" && url.startsWith("/api/")) {
          init = Object.assign({}, init, { headers: Object.assign({}, init && init.headers, { "X-Janet-Client": me }) });
        }
        return plainFetch(input, init);
      };
    }

    function holderName(s) {
      const h = s && s.holder;
      return h ? h.name || h.client : "";
    }

    function render() {
      const s = snap;
      const mine = !s || s.you === "controller";
      const other = holderName(s);
      let msg = "";
      if (!mine) {
        if (s.you === "observer") msg = other ? "Watching read-only. " + other + " controls this session." : "Watching read-only. Nobody controls this session.";
        else if (other) msg = other + " controls this session. Changes from this page are refused.";
      }
      bar.hidden = !msg;
      text.textContent = msg;
      takeBtn.disabled = !!(s && s.busy && other);
      takeBtn.title = takeBtn.disabled ? "Control can change hands when the running turn ends" : "";
    }

    async function take() {
      let r;
      try {
        r = await fetch("/api/control", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ action: "take" }) });
      } catch (_) {
        text.textContent = "Cannot reach janetcode. Is it still running?";
        return;
      }
      const body = await r.json().catch(() => null);
      if (r.ok) {
        // An observer's stream was attached read-only: reload as a controller.
        if (observing) location.search = "";
        else apply(body);
        return;
      }
      if (body && body.conflict) apply(body.conflict.state);
      if (body && body.error) text.textContent = body.error;
    }
    takeBtn.addEventListener("click", take);

    function apply(c) {
      if (!c) return;
      snap = c;
      render();
    }

    // explain turns the text of a refused request into something to show:
    // a conflict carries its message and the state to show with it.
    function explain(body) {
      try {
        const j = JSON.parse(body);
        if (j && j.conflict) {
          apply(j.conflict.state);
          return j.error;
        }
      } catch (_) { /* a plain-text error */ }
      return body;
    }

    return { apply, explain, observing, eventsURL: observing ? "/events?role=observer&client=" + encodeURIComponent(me) : "/events" };
  };
})(window);
