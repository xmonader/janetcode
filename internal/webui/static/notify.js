// Desktop notifications and the ding for a turn that finishes or asks for
// approval while the tab is hidden, plus the Settings toggle. Reads the
// shared view model (S.lastReply) and calls back into app.js only for
// markUnseen (the tab-title dot).
(function (global) {
  "use strict";
  global.JanetNotify = function ({ S, settings, markUnseen }) {
    const { ding, quiet } = settings;
    const notifBtn = document.getElementById("notif-btn");
    const notifSupported = "Notification" in window;
    let notifyOn = notifSupported && localStorage.getItem("janetcode-notify") === "1" && Notification.permission === "granted";
    function drawNotif() {
      notifBtn.textContent = !notifSupported ? "Notifications unsupported"
        : notifyOn ? "Notifications on" : "Notifications off";
      notifBtn.classList.toggle("on", notifyOn);
    }
    notifBtn.addEventListener("click", async () => {
      if (!notifSupported) return;
      if (notifyOn) {
        notifyOn = false;
        localStorage.setItem("janetcode-notify", "");
      } else {
        const perm = await Notification.requestPermission();
        notifyOn = perm === "granted";
        localStorage.setItem("janetcode-notify", notifyOn ? "1" : "");
      }
      drawNotif();
    });
    drawNotif();
    function maybeNotify(prevRunning, st) {
      if (!prevRunning || st.running || !document.hidden) return;
      markUnseen();
      if (settings.soundOn()) ding();
      if (!notifyOn || quiet()) return;
      const title = document.getElementById("title").textContent || "janetcode";
      const body = S.lastReply ? S.lastReply.split("\n")[0].slice(0, 120) : (st.status || "Turn finished");
      try { new Notification("janetcode · " + title, { body }); } catch (_) {}
    }

    // A turn waiting on an approval in a tab you are not looking at looks
    // idle; the terminal UI rings for it, and so does the page. One tag, so
    // a second request replaces the first instead of stacking.
    function notifyAsk(line) {
      if (!document.hidden) return;
      if (settings.soundOn()) ding();
      if (!notifyOn || quiet()) return;
      try { new Notification("janetcode · approval needed", { body: line, tag: "janetcode-ask" }); } catch (_) {}
    }
    return { maybeNotify, notifyAsk };
  };
})(window);
