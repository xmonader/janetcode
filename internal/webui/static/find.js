// The find bar: Ctrl+F searches the conversation itself. app.js passes the
// scroller (the element whose text is searched) and the composer (focus goes
// back to it on Esc); the palette reaches openFind through the return value.
(function (global) {
  "use strict";
  global.JanetFind = function ({ scroller, input }) {
    // The find bar: Ctrl+F searches the conversation itself — every text
    // node in the thread, highlighted, with next/prev like the browser's
    // own find but able to see inside rendered markdown.
    const findbar = document.getElementById("findbar");
    const findQ = document.getElementById("find-q");
    const findCount = document.getElementById("find-count");
    let findHits = [];
    let findIdx = -1;

    function openFind() {
      findbar.hidden = false;
      findQ.focus();
      findQ.select();
      if (findQ.value) findRender(findQ.value);
    }
    function closeFind() {
      findbar.hidden = true;
      findClear();
    }
    function findClear() {
      for (const h of findHits) h.replaceWith(document.createTextNode(h.textContent));
      findHits = [];
      findIdx = -1;
      findCount.textContent = "";
    }
    function findRender(q) {
      findClear();
      q = q.trim();
      if (!q) return;
      const root = scroller();
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
        acceptNode(n) {
          if (!n.nodeValue.trim()) return NodeFilter.FILTER_REJECT;
          const p = n.parentElement;
          if (!p || p.closest("textarea, style, script")) return NodeFilter.FILTER_REJECT;
          return NodeFilter.FILTER_ACCEPT;
        },
      });
      const nodes = [];
      while (walker.nextNode()) nodes.push(walker.currentNode);
      const lower = q.toLowerCase();
      for (const node of nodes) {
        const text = node.nodeValue;
        const tl = text.toLowerCase();
        const frag = document.createDocumentFragment();
        let i = 0,
          hit,
          last = 0;
        while ((hit = tl.indexOf(lower, i)) !== -1) {
          frag.appendChild(document.createTextNode(text.slice(last, hit)));
          const span = document.createElement("span");
          span.className = "find-hit";
          span.textContent = text.slice(hit, hit + q.length);
          frag.appendChild(span);
          findHits.push(span);
          last = hit + q.length;
          i = hit + q.length;
          if (findHits.length >= 500) break;
        }
        if (last) {
          frag.appendChild(document.createTextNode(text.slice(last)));
          node.parentNode.replaceChild(frag, node);
        }
        if (findHits.length >= 500) break;
      }
      if (!findHits.length) {
        findCount.textContent = "no matches";
        return;
      }
      findPaint(0);
    }
    function findPaint(i) {
      findIdx = (i + findHits.length) % findHits.length;
      findHits.forEach((h, j) => h.classList.toggle("on", j === findIdx));
      findHits[findIdx].scrollIntoView({ block: "center" });
      findCount.textContent = findIdx + 1 + "/" + findHits.length;
    }
    document.addEventListener("keydown", (ev) => {
      if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "f") {
        ev.preventDefault();
        if (findbar.hidden) openFind();
        else closeFind();
        return;
      }
      if (!findbar.hidden && ev.key === "Escape") {
        ev.preventDefault();
        ev.stopImmediatePropagation();
        closeFind();
        input.focus();
      }
    });
    findQ.addEventListener("input", () => findRender(findQ.value));
    findQ.addEventListener("keydown", (ev) => {
      ev.stopPropagation(); // the composer must not read these as its own
      if (ev.key === "Enter") {
        ev.preventDefault();
        findPaint(findIdx + (ev.shiftKey ? -1 : 1));
      }
      if (ev.key === "ArrowUp") { ev.preventDefault(); findPaint(findIdx - 1); }
      if (ev.key === "ArrowDown") { ev.preventDefault(); findPaint(findIdx + 1); }
    });
    document.getElementById("find-prev").addEventListener("click", () => findPaint(findIdx - 1));
    document.getElementById("find-next").addEventListener("click", () => findPaint(findIdx + 1));
    document.getElementById("find-close").addEventListener("click", closeFind);

    return { open: openFind, close: closeFind };
  };
})(window);
