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
  const r = await cmd('Runtime.evaluate', { expression, returnByValue: true });
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

console.log(results.join('\n'));
const failed = results.filter(r => r.startsWith('FAIL'));
console.log(failed.length ? `${failed.length} FAILURES` : 'ALL PASS');
ws.close();
process.exit(failed.length ? 1 : 0);
