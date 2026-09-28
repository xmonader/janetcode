/* janetcode web — markdown rendering module (browser + node).
   Replaces the hand-rolled regex renderer with markdown-it (MIT,
   vendored): proper CommonMark parsing, GFM tables, and sanitization
   by construction (html:false — raw HTML is escaped, links are
   scheme-validated). Code fences render as the app's codeblock widget
   (language label, wrap/copy, highlight.js for finished blocks, the
   small generic tokenizer for the streaming tail and unknown
   languages).

   Rendering is block-incremental with a cache: the thread re-renders on
   every SSE chunk, and only the growing tail block changes, so earlier
   blocks — including their highlight.js output — are reused. Exposes
   window.JanetMD (or module.exports under node, for the golden tests). */
(function (global) {
  "use strict";

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }

  // Code blocks keep their raw source here, keyed by a generated id on
  // the Copy button — attribute-escaping megabytes of output onto a DOM
  // node is slower and leakier than a lookup.
  const codeBlocks = new Map();
  let codeSeq = 0;

  // highlight is a deliberately small tokenizer: comments, strings,
  // numbers and keywords, colored generically so one pass serves every
  // language the model writes. It covers the streaming tail and any
  // language highlight.js does not bundle; everything it emits is
  // escaped first.
  function highlight(src, lang) {
    const comments = { js: "//", ts: "//", javascript: "//", typescript: "//", go: "//", rust: "//", java: "//", c: "//", cpp: "//", csharp: "//", swift: "//", kt: "//", kotlin: "//", scala: "//", php: "//", sh: "#", bash: "#", shell: "#", zsh: "#", py: "#", python: "#", ruby: "#", rb: "#", yaml: "#", yml: "#", tf: "#", hcl: "#", toml: "#" };
    const c = (comments[(lang || "").toLowerCase()] || "#").replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const kw = new Set(("break case catch chan class const continue default defer defer do else enum export extends finally for from func function go if import in instanceof interface let map new of package private protected public range return select static super switch this throw try type typeof var void while with yield async await def fn impl pub struct self mut ref match lambda pass raise global assert del elif except nonlocal as is and or not None True False print println int str bool float char double long short unsigned void null true false").split(" "));
    const re = new RegExp(
      "(" + c + "[^\\n]*)" +
      "|('(?:[^'\\\\\\n]|\\\\.)*'|\"(?:[^\"\\\\\\n]|\\\\.)*\"|`(?:[^`\\\\]|\\\\.)*`)" +
      "|\\b(0x[0-9a-fA-F]+|\\d+(?:\\.\\d+)?)\\b" +
      "|\\b([A-Za-z_$][\\w$]*)\\b",
      "g"
    );
    let out = "", last = 0;
    for (let m; (m = re.exec(src));) {
      out += esc(src.slice(last, m.index));
      if (m[1]) out += `<span class="tc">${esc(m[1])}</span>`;
      else if (m[2]) out += `<span class="ts">${esc(m[2])}</span>`;
      else if (m[3]) out += `<span class="tn">${esc(m[3])}</span>`;
      else out += kw.has(m[4]) ? `<span class="tk">${esc(m[4])}</span>` : esc(m[4]);
      last = m.index + m[0].length;
    }
    return out + esc(src.slice(last));
  }

  // highlight.js (BSD-3, vendored) for finished blocks and tool output.
  const hljsCache = new Map();
  const HLJS_MAX = 300;
  const HLJS_ALIAS = {
    js: "javascript", jsx: "javascript", mjs: "javascript", cjs: "javascript",
    ts: "typescript", tsx: "typescript", mts: "typescript", cts: "typescript",
    py: "python", gyp: "python",
    sh: "bash", shell: "bash", zsh: "bash", shellscript: "bash", dockerfile: "bash",
    yml: "yaml", tf: "ini", hcl: "ini", toml: "ini",
    rs: "rust", kt: "kotlin", kts: "kotlin", cs: "csharp",
    golang: "go", rb: "ruby", pl: "perl",
    html: "xml", htm: "xml", vue: "xml", svg: "xml",
    mk: "makefile",
  };
  function hljsLang(lang) {
    const name = HLJS_ALIAS[(lang || "").toLowerCase()] || String(lang || "").toLowerCase();
    if (name && global.hljs && global.hljs.getLanguage(name)) return name;
    return "";
  }
  function hljsHighlight(code, lang) {
    const l = hljsLang(lang);
    if (!l) return highlight(code, lang);
    const key = l + " " + code;
    let html = hljsCache.get(key);
    if (html == null) {
      if (hljsCache.size >= HLJS_MAX) hljsCache.delete(hljsCache.keys().next().value);
      try {
        html = global.hljs.highlight(code, { language: l }).value;
      } catch (_) {
        return highlight(code, lang);
      }
      hljsCache.set(key, html);
    }
    return html;
  }

  let currentFinal = false;
  function codeBlockHTML(lang, code, final) {
    const bid = "cb" + ++codeSeq;
    codeBlocks.set(bid, code);
    if (tailBids) tailBids.push(bid);
    // Streaming renders the growing block with the cheap tokenizer;
    // finished blocks and tool output get the real highlighter.
    const body = final ? hljsHighlight(code, lang) : highlight(code, lang);
    const wrap = `<button type="button" class="codecopy" data-wrap="${bid}" title="Toggle word wrap">Wrap</button>`;
    return (
      `<div class="codeblock" data-lang="${esc(lang || "")}"><div class="codehead"><span>${esc(lang || "code")}</span>` +
      `${wrap}<button type="button" class="codecopy" data-bid="${bid}" title="Copy code">Copy</button></div>` +
      `<pre><code class="${lang ? "lang-" + esc(lang) : ""}">${body}</code></pre></div>`
    );
  }

  let mdi = null;
  function mdit() {
    if (mdi) return mdi;
    if (!global.markdownit) return null;
    mdi = global.markdownit({ html: false, breaks: false, linkify: false, typographer: false, langPrefix: "" });
    // Fences become the app's codeblock widget (label + wrap/copy +
    // mermaid detection via data-lang), not a bare <pre>.
    mdi.renderer.rules.fence = (tokens, idx) => {
      const info = (tokens[idx].info || "").trim();
      const lang = info ? info.split(/\s+/)[0] : "";
      return codeBlockHTML(lang, tokens[idx].content.replace(/\n$/, ""), currentFinal);
    };
    // External links open in a tab; markdown-it's validateLink already
    // refuses javascript:/data: schemes.
    const linkOpen = mdi.renderer.rules.link_open ||
      ((tokens, idx, options, env, self) => self.renderToken(tokens, idx, options));
    mdi.renderer.rules.link_open = (tokens, idx, options, env, self) => {
      const href = tokens[idx].attrGet("href") || "";
      if (/^https?:\/\//i.test(href)) {
        tokens[idx].attrSet("target", "_blank");
        tokens[idx].attrSet("rel", "noopener");
      }
      return linkOpen(tokens, idx, options, env, self);
    };
    return mdi;
  }

  // splitBlocks cuts the source at blank lines, but never inside a
  // fenced region, so each piece is independently parseable. A fence
  // closes the way CommonMark closes it: the same character, at least as
  // many of them, and nothing after but spaces. Closing on any fence
  // line cut a ````markdown example at its inner ```js line and rendered
  // the rest of the example as prose.
  function splitBlocks(t) {
    const lines = t.split("\n");
    const blocks = [];
    let cur = [], inFence = false, fenceMark = "", fenceLen = 0;
    for (const line of lines) {
      const f = /^\s{0,3}(`{3,}|~{3,})(.*)$/.exec(line);
      if (f) {
        const mark = f[1][0];
        if (!inFence) { inFence = true; fenceMark = mark; fenceLen = f[1].length; cur.push(line); continue; }
        if (mark === fenceMark && f[1].length >= fenceLen && !f[2].trim()) {
          cur.push(line); blocks.push(cur.join("\n")); cur = []; inFence = false; fenceMark = ""; fenceLen = 0; continue;
        }
        cur.push(line); continue;
      }
      if (!inFence && /^\s*$/.test(line)) {
        if (cur.length) { blocks.push(cur.join("\n")); cur = []; }
        continue;
      }
      cur.push(line);
    }
    if (cur.length) blocks.push(cur.join("\n"));
    return blocks;
  }

  const blockCache = new Map();
  const BLOCK_MAX = 800;
  // The last block of a streaming reply is different on every chunk, so
  // it is rendered fresh and never cached, and the code it registered
  // for its Copy button is dropped when the next chunk replaces it.
  // Cached and kept, a 3,000-line block streamed a line at a time held
  // every prefix of itself: 40 MB in codeBlocks alone.
  let tailBids = null;
  let lastTailBids = [];
  function render(src, final) {
    src = String(src ?? "");
    currentFinal = !!final;
    const parser = mdit();
    if (!parser) return `<p>${esc(src)}</p>`;
    if (!final) {
      for (const bid of lastTailBids) codeBlocks.delete(bid);
      lastTailBids = [];
    }
    const out = [];
    const blocks = splitBlocks(src);
    blocks.forEach((block, i) => {
      if (!final && i === blocks.length - 1) {
        tailBids = lastTailBids;
        try { out.push(parser.render(block)); } finally { tailBids = null; }
        return;
      }
      const key = (final ? "F" : "S") + block;
      let html = blockCache.get(key);
      if (html == null) {
        if (blockCache.size >= BLOCK_MAX) blockCache.delete(blockCache.keys().next().value);
        html = parser.render(block);
        blockCache.set(key, html);
      }
      out.push(html);
    });
    return out.join("");
  }

  const CODE_EXT = /\.(py|go|js|mjs|ts|tsx|jsx|rs|java|c|h|cpp|hpp|cs|rb|php|sh|bash|sql|yaml|yml|json|toml|html|css|scss|lua|swift|kt|kts|scala)(\W|$)/;
  function langOf(name, summary) {
    const m = CODE_EXT.exec(summary || "");
    if (m) return m[1] === "mjs" ? "js" : m[1];
    if (name === "bash" || name === "shell" || name === "run") return "sh";
    return "";
  }
  // A tool result's body: highlighted when the tool was reading a file
  // whose language we know, plain otherwise. A long output stays plain —
  // the tokenizer is fast, but not on a megabyte.
  function renderOutput(name, summary, output) {
    const lang = langOf(name || "", summary || "");
    if (lang && (output || "").length < 200_000) return hljsHighlight(output, lang);
    return esc(output || "");
  }

  const api = { render, renderOutput, highlight, hljsHighlight, codeBlocks, esc };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  global.JanetMD = api;
})(typeof window !== "undefined" ? window : globalThis);
