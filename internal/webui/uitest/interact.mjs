// Drive the real webui in headless Chrome over CDP: toggles + drag-resize.
// Assertions, not eyeballs — run: node interact.mjs <url>
// Requires chrome with --remote-debugging-port=9223 (see make ui-test).
const CDP_PORT = 9223;
const APP = process.argv[2] || 'http://127.0.0.1:8787/';
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

async function getWsUrl() {
  for (let i = 0; i < 40; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?${APP}`, { method: 'PUT' });
      return (await r.json()).webSocketDebuggerUrl;
    } catch (_) { await sleep(250); }
  }
  throw new Error('chrome not up');
}

const ws = new WebSocket(await getWsUrl());
await new Promise(r => ws.onopen = r);
let seq = 0;
const pending = new Map();
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
};
const cmd = (method, params = {}) => new Promise((res) => {
  const id = ++seq;
  pending.set(id, res);
  ws.send(JSON.stringify({ id, method, params }));
});
const evaljs = async (expression) => {
  const r = await cmd('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (r.result.exceptionDetails) throw new Error('page JS error: ' + JSON.stringify(r.result.exceptionDetails.exception?.description || r.result.exceptionDetails.text));
  return r.result.result.value;
};

await sleep(2500); // let app.js boot, SSE settle
const results = [];
const check = (name, ok, extra = '') => { results.push(`${ok ? 'PASS' : 'FAIL'} ${name}${extra ? ' — ' + extra : ''}`); };

// 0. the page carries the server's stamped build (stale-UI toast data)
check('build stamped into page', await evaljs(`window.__janetUIBuild && window.__janetUIBuild !== "__JANET_UI_BUILD__"`), await evaljs(`window.__janetUIBuild`));

// 1. handles exist and are visible
check('nav handle visible', await evaljs(`!!document.getElementById('nav-resize') && getComputedStyle(document.getElementById('nav-resize')).cursor === 'col-resize'`));

// 2. nav drag +80px
const navBefore = await evaljs(`document.querySelector('.nav').getBoundingClientRect().width`);
let h = await evaljs(`(() => { const r = document.getElementById('nav-resize').getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + 200 }; })()`);
await cmd('Input.dispatchMouseEvent', { type: 'mousePressed', x: h.x, y: h.y, button: 'left', buttons: 1, clickCount: 1 });
for (let i = 1; i <= 8; i++) await cmd('Input.dispatchMouseEvent', { type: 'mouseMoved', x: h.x + i * 10, y: h.y, button: 'left', buttons: 1 });
await cmd('Input.dispatchMouseEvent', { type: 'mouseReleased', x: h.x + 80, y: h.y, button: 'left', buttons: 0, clickCount: 1 });
const navVar = await evaljs(`getComputedStyle(document.documentElement).getPropertyValue('--nav-w').trim()`);
check('nav drag widens rail', Math.abs(parseInt(navVar) - (navBefore + 80)) <= 2, `--nav-w=${navVar}, was ${navBefore}`);

// 3. nav double-click resets
h = await evaljs(`(() => { const r = document.getElementById('nav-resize').getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + 200 }; })()`);
await cmd('Input.dispatchMouseEvent', { type: 'mousePressed', x: h.x, y: h.y, button: 'left', buttons: 1, clickCount: 1 });
await cmd('Input.dispatchMouseEvent', { type: 'mouseReleased', x: h.x, y: h.y, button: 'left', buttons: 0, clickCount: 1 });
await cmd('Input.dispatchMouseEvent', { type: 'mousePressed', x: h.x, y: h.y, button: 'left', buttons: 1, clickCount: 2 });
await cmd('Input.dispatchMouseEvent', { type: 'mouseReleased', x: h.x, y: h.y, button: 'left', buttons: 0, clickCount: 2 });
const navAfterReset = await evaljs(`getComputedStyle(document.documentElement).getPropertyValue('--nav-w').trim()`);
check('nav dbl-click resets', Math.abs(parseInt(navAfterReset) - 268) <= 1, `--nav-w=${navAfterReset}`);

// 4. work drawer toggle (was covered by its own drawer)
await evaljs(`document.getElementById('work-toggle').click()`);
await sleep(400);
const workOpen = await evaljs(`!document.querySelector('.shell').classList.contains('work-collapsed') && getComputedStyle(document.getElementById('work')).transform === 'none'`);
check('work drawer opens from header toggle', workOpen);
const workBefore = await evaljs(`document.getElementById('work').getBoundingClientRect().width`);

// 5. drag the work drawer wider by 60px (drag left = wider)
h = await evaljs(`(() => { const r = document.getElementById('work-resize').getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + 200 }; })()`);
await cmd('Input.dispatchMouseEvent', { type: 'mousePressed', x: h.x, y: h.y, button: 'left', buttons: 1, clickCount: 1 });
for (let i = 1; i <= 6; i++) await cmd('Input.dispatchMouseEvent', { type: 'mouseMoved', x: h.x - i * 10, y: h.y, button: 'left', buttons: 1 });
await cmd('Input.dispatchMouseEvent', { type: 'mouseReleased', x: h.x - 60, y: h.y, button: 'left', buttons: 0, clickCount: 1 });
const workAfter = await evaljs(`document.getElementById('work').getBoundingClientRect().width`);
check('work drawer drag resizes', Math.abs(workAfter - (workBefore + 60)) <= 2, `${workBefore} -> ${workAfter}`);

// 6. work drawer closes again from the same toggle
await evaljs(`document.getElementById('work-toggle').click()`);
await sleep(400);
const workClosed = await evaljs(`document.querySelector('.shell').classList.contains('work-collapsed')`);
check('work drawer hides again', workClosed);

// 7. preview toggle both ways (was stuck: read hidden attr)
await evaljs(`document.getElementById('preview-toggle').click()`);
await sleep(400);
check('preview opens', await evaljs(`document.querySelector('.shell').classList.contains('pv-open') && getComputedStyle(document.getElementById('preview')).transform === 'none'`));
await evaljs(`document.getElementById('preview-toggle').click()`);
await sleep(400);
check('preview hides again', await evaljs(`!document.querySelector('.shell').classList.contains('pv-open')`));

// 8. the thread stays pinned to its tail while content grows under it
const grow = (n) => evaljs(`(() => { const log = document.getElementById('log'); for (let i = 0; i < ${n}; i++) { const d = document.createElement('div'); d.className = 'turn'; d.style.height = '120px'; d.textContent = 'filler ' + i; log.appendChild(d); } return 1; })()`);
const gap = () => evaljs(`(() => { const el = document.getElementById('chat-view'); return el.scrollHeight - el.scrollTop - el.clientHeight; })()`);
await evaljs(`document.getElementById('empty').hidden = true; 1`);
await grow(20);
await sleep(150);
check('pinned thread follows growth', (await gap()) < 2, `gap=${await gap()}`);
await grow(5);
await sleep(150);
check('pinned thread follows again', (await gap()) < 2, `gap=${await gap()}`);

// 9. scrolling up unpins: further growth leaves the reader where they are
await evaljs(`(() => { const el = document.getElementById('chat-view'); el.scrollTop = 200; el.dispatchEvent(new Event('scroll')); return 1; })()`);
await sleep(100);
await grow(5);
await sleep(150);
const top = await evaljs(`document.getElementById('chat-view').scrollTop`);
check('reader scrolled up stays put', Math.abs(top - 200) <= 2, `scrollTop=${top}`);
check('jump-to-latest offered when away', await evaljs(`!document.getElementById('to-latest').hidden`));

// 10. messages and composer share one column, drawer open or not
const edges = () => evaljs(`(() => { const a = document.getElementById('log').getBoundingClientRect(), b = document.getElementById('composer').getBoundingClientRect(); return [Math.round(a.left - b.left), Math.round(a.right - b.right)]; })()`);
let e = await edges();
check('composer aligned with thread', Math.abs(e[0]) <= 1 && Math.abs(e[1]) <= 1, JSON.stringify(e));
await evaljs(`document.getElementById('work-toggle').click()`);
await sleep(450);
e = await edges();
check('composer aligned with drawer open', Math.abs(e[0]) <= 1 && Math.abs(e[1]) <= 1, JSON.stringify(e));
await evaljs(`document.getElementById('work-toggle').click()`);
await sleep(450);
check('closed drawer is not rendered', await evaljs(`getComputedStyle(document.getElementById('work')).visibility === 'hidden'`));

// 11. no text glyph stands in for an icon on a header, rail or composer button
const glyphs = await evaljs(`[...document.querySelectorAll('.top button, .nav-foot button, .composer-bar button, .findbar button, #to-latest')].filter(b => !b.querySelector('svg') && !/[A-Za-z]/.test(b.textContent)).map(b => b.id || b.className)`);
check('icon buttons draw SVG, not glyphs', glyphs.length === 0, glyphs.join(','));

// 11b. the vendored faces load and are the ones in use
check('Inter and JetBrains Mono load', await evaljs(`document.fonts.ready.then(() => document.fonts.check('14px Inter') && document.fonts.check('14px "JetBrains Mono"') && [...document.fonts].some(f => f.family.replace(/"/g, '') === 'Inter' && f.status === 'loaded'))`));
check('body text uses Inter', await evaljs(`getComputedStyle(document.body).fontFamily.startsWith('Inter')`));

// 12. phone width: the rail starts shut, and a tap on the dim closes it
await cmd('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
await evaljs(`localStorage.setItem('janetcode-nav-collapsed', ''); location.reload(); 1`);
await sleep(2000);
check('phone rail starts closed', await evaljs(`document.querySelector('.shell').classList.contains('nav-collapsed')`));
await evaljs(`document.getElementById('nav-open').click()`);
await sleep(300);
check('phone rail opens', await evaljs(`!document.querySelector('.shell').classList.contains('nav-collapsed')`));
await cmd('Input.dispatchMouseEvent', { type: 'mousePressed', x: 370, y: 400, button: 'left', buttons: 1, clickCount: 1 });
await cmd('Input.dispatchMouseEvent', { type: 'mouseReleased', x: 370, y: 400, button: 'left', buttons: 0, clickCount: 1 });
await sleep(300);
check('tap outside closes phone rail', await evaljs(`document.querySelector('.shell').classList.contains('nav-collapsed')`));
check('phone rail state not persisted', await evaljs(`localStorage.getItem('janetcode-nav-collapsed') === ''`));

console.log(results.join('\n'));
const failed = results.filter(r => r.startsWith('FAIL'));
console.log(failed.length ? `${failed.length} FAILURES` : 'ALL PASS');
ws.close();
process.exit(failed.length ? 1 : 0);
