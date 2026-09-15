// Adds a "copy" button to every code block. Vanilla JS, no dependencies,
// no network access. Fails silently if the clipboard API is unavailable
// (e.g. file:// in some browsers), since the page works without it.
(function () {
  "use strict";

  function addButtons() {
    var blocks = document.querySelectorAll("pre code");
    for (var i = 0; i < blocks.length; i++) {
      var codeEl = blocks[i];
      var pre = codeEl.parentElement;
      if (!pre || pre.dataset.copyReady) continue;
      pre.dataset.copyReady = "1";

      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "copy-btn";
      btn.textContent = "copy";
      btn.setAttribute("aria-label", "Copy code to clipboard");

      btn.addEventListener("click", function (evt) {
        var target = evt.currentTarget;
        var code = target.previousElementSibling
          ? target.parentElement.querySelector("code")
          : null;
        if (!code) return;
        var text = code.textContent;
        copyText(text, target);
      });

      pre.appendChild(btn);
    }
  }

  function copyText(text, btn) {
    var done = function (ok) {
      var original = "copy";
      btn.textContent = ok ? "copied" : "press ctrl+c";
      window.setTimeout(function () {
        btn.textContent = original;
      }, 1500);
    };
    try {
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(
          function () { done(true); },
          function () { done(false); }
        );
        return;
      }
    } catch (e) {
      /* fall through to the legacy path below */
    }
    try {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      var ok = document.execCommand("copy");
      document.body.removeChild(ta);
      done(ok);
    } catch (e) {
      done(false);
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", addButtons);
  } else {
    addButtons();
  }
})();
