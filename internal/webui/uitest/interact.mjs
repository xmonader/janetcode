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

// 8b. live thinking is a short window anchored to its newest line, not a
// two-line fragment: the last line is on screen, the first scrolled off.
const live = await evaljs(`(() => {
  const n = document.createElement('div'); n.className = 'turn thought';
  const lines = Array.from({length: 20}, (_, i) => 'thinking line ' + i).join('\\n');
  n.innerHTML = '<span class="lbl">Thinking</span><div class="thought-live"><pre class="thought-body"></pre></div>';
  n.querySelector('pre').textContent = lines;
  document.getElementById('log').appendChild(n);
  const box = n.querySelector('.thought-live').getBoundingClientRect();
  const pre = n.querySelector('pre').getBoundingClientRect();
  const lh = parseFloat(getComputedStyle(n.querySelector('pre')).lineHeight) || 16;
  const r = { boxH: box.height, lh, lastVisible: Math.abs(pre.bottom - box.bottom) < 1, firstHidden: pre.top < box.top - lh };
  n.remove();
  return r;
})()`);
check('live thinking shows its newest lines', live.lastVisible && live.firstHidden && live.boxH > 3 * live.lh && live.boxH < 8 * live.lh, JSON.stringify(live));

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

// 13. a turn that ends while the tab is in the background marks the tab
// title, and looking at the tab clears it. The stub provider refuses the
// connection, so the turn ends fast — in an error, which still ends it.
await cmd('Emulation.setDeviceMetricsOverride', { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
await evaljs(`location.reload(); 1`);
await sleep(2000);
await evaljs(`(() => { window.__hidden = false; Object.defineProperty(document, 'hidden', { configurable: true, get: () => window.__hidden }); return 1; })()`);
await evaljs(`(() => { window.__hidden = true; const t = document.getElementById('input'); t.value = 'hello'; t.dispatchEvent(new Event('input', { bubbles: true })); document.getElementById('send').click(); return 1; })()`);
let badged = false;
for (let i = 0; i < 40 && !badged; i++) { await sleep(500); badged = await evaljs(`document.title.startsWith('● ')`); }
check('background turn end badges the tab title', badged, await evaljs(`document.title`));
await evaljs(`(() => { window.__hidden = false; document.dispatchEvent(new Event('visibilitychange')); return 1; })()`);
await sleep(100);
check('looking at the tab clears the badge', await evaljs(`!document.title.startsWith('●')`), await evaljs(`document.title`));

// 14. a permission card with a rule picker does not also print the rule
// on a line of its own; one without a picker still names the rule.
await cmd('Page.enable');
await cmd('Page.addScriptToEvaluateOnNewDocument', { source: `(() => { const ES = window.EventSource; window.EventSource = function (u) { const e = new ES(u); window.__es = e; return e; }; window.EventSource.prototype = ES.prototype; })()` });
await evaljs(`location.reload(); 1`);
await sleep(2000);
const ask = (perm) => evaljs(`(() => { window.__es.onmessage({ data: JSON.stringify({ type: 'permission', permission: ${JSON.stringify(perm)} }) }); const r = document.getElementById('ask-rule'); return { rule: !r.hidden, text: r.textContent, picker: !document.getElementById('ask-rungs').hidden }; })()`);
const withPicker = await ask({ name: 'bash', summary: 'python -m pytest', rule: 'bash(python *)', rungs: [{ rule: 'bash(python -m pytest *)' }, { rule: 'bash(python *)' }], ruleIndex: 1 });
check('rule picker shown without a duplicate rule line', withPicker.picker && !withPicker.rule, JSON.stringify(withPicker));
const noPicker = await ask({ name: 'edit_file', summary: 'a.go', rule: 'edit_file' });
check('rule line shown when there is no picker', !noPicker.picker && noPicker.rule && noPicker.text.includes('edit_file'), JSON.stringify(noPicker));

// 15. a provider failure is laid out as what failed, what to do and the
// code; a failure a retry cannot fix offers no Retry button.
const errCard = (x) => evaljs(`(() => { window.__es.onmessage({ data: JSON.stringify({ type: 'entry', entry: { id: 'e-' + Math.random(), kind: 'error', error: true, text: 'x', data: ${JSON.stringify(x)} } }) }); const n = [...document.querySelectorAll('.turn.err')].pop(); return { head: n.querySelector('.err-head')?.firstChild?.textContent, next: n.querySelector('.err-next')?.textContent, meta: n.querySelector('.err-meta')?.textContent, retry: !!n.querySelector('.retry-btn') }; })()`);
await evaljs(`(() => { const t = document.getElementById('input'); t.value = 'hi'; return 1; })()`);
const auth = await errCard({ headline: 'Authentication failed', next: 'Check the API key for fake in the config file.', code: 'HTTP 401', reason: 'Unauthorized', retry: false });
check('error card reads headline, next step, code', auth.head === 'Authentication failed' && auth.next.startsWith('Check the API key') && auth.meta === 'HTTP 401 · Unauthorized', JSON.stringify(auth));
check('no Retry where a retry cannot help', !auth.retry, JSON.stringify(auth));

// 16. a long working directory keeps its end — the project name — and
// the full path is one hover away.
const cwdShown = await evaljs(`(() => { window.__es.onmessage({ data: JSON.stringify({ type: 'hello', hello: { cwd: '/tmp/claude-1000/-home-x-wspace-janetcode/f8c3ae96/scratchpad/dog/proj/py-bug', cwd_short: '/tmp/claude-1000/-home-x-wspace-janetcode/f8c3ae96/scratchpad/dog/proj/py-bug', model: 'm', provider: 'p', version: window.__janetUIBuild } }) }); const c = document.getElementById('cwd'); return { text: c.textContent, title: c.title }; })()`);
check('long cwd keeps the project end', cwdShown.text === '…/dog/proj/py-bug' && cwdShown.title.endsWith('/py-bug'), JSON.stringify(cwdShown));
check('session times are not set in monospace', await evaljs(`(() => { const d = document.createElement('span'); d.className = 'when'; document.getElementById('sessions').appendChild(d); const f = getComputedStyle(d).fontFamily; d.remove(); return !/Mono|monospace/.test(f); })()`));

// 17. keyboard and screen reader. Every control a pointer can use has a
// name a screen reader can say, and Tab reaches the ones a session needs.
const unnamed = await evaljs(`(() => {
  const vis = (el) => el.offsetParent !== null && getComputedStyle(el).visibility !== 'hidden';
  const nameOf = (el) => {
    if (el.getAttribute('aria-label')) return el.getAttribute('aria-label');
    const by = el.getAttribute('aria-labelledby');
    if (by) return by.split(' ').map(id => document.getElementById(id)?.textContent || '').join(' ').trim();
    if (el.labels && el.labels.length) return [...el.labels].map(l => l.textContent).join(' ').trim();
    return (el.textContent || '').trim() || el.getAttribute('title') || '';
  };
  return [...document.querySelectorAll('button, a[href], input:not([type=hidden]), select, textarea, [role=button], [role=tab]')]
    .filter(vis).filter(el => !nameOf(el)).map(el => el.id || el.className || el.tagName).slice(0, 12);
})()`);
check('every visible control has an accessible name', unnamed.length === 0, JSON.stringify(unnamed));
const landmarks = await evaljs(`(() => ({
  log: document.getElementById('log').getAttribute('role'),
  announcer: document.getElementById('sr-announce')?.getAttribute('aria-live'),
  ask: document.getElementById('ask').getAttribute('role'),
  palette: document.getElementById('palette').getAttribute('role'),
  paletteLabel: document.getElementById('palette').getAttribute('aria-label'),
}))()`);
check('the conversation is a log, announced through one polite live region', landmarks.log === 'log' && landmarks.announcer === 'polite', JSON.stringify(landmarks));
check('the approval card and the palette are labelled dialogs', landmarks.ask === 'alertdialog' && landmarks.palette === 'dialog' && !!landmarks.paletteLabel, JSON.stringify(landmarks));

// Tab from the top of the page: the composer, send, the model, the
// palette, settings and help must all be reachable, each with a visible
// focus ring.
await evaljs(`document.activeElement && document.activeElement.blur(); document.body.focus(); 1`);
const reached = new Set();
let ringless = [];
for (let i = 0; i < 90; i++) {
  await cmd('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 });
  await cmd('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 });
  const f = await evaljs(`(() => { const a = document.activeElement; if (!a || a === document.body) return null; const cs = getComputedStyle(a); return { id: a.id || a.getAttribute('aria-label') || a.className, ring: (cs.outlineStyle !== 'none' && cs.outlineWidth !== '0px') || cs.boxShadow !== 'none' || a.tagName === 'TEXTAREA' }; })()`);
  if (!f) continue;
  reached.add(f.id);
  if (!f.ring) ringless.push(f.id);
}
const need = ['input', 'send', 'model', 'search-btn', 'settings-btn', 'help-btn'];
const missing = need.filter(id => !reached.has(id));
check('Tab reaches the composer, send, model, palette, settings and help', missing.length === 0, 'missing ' + JSON.stringify(missing));
ringless = [...new Set(ringless)];
check('every control Tab lands on shows where focus is', ringless.length === 0, JSON.stringify(ringless.slice(0, 10)));

// A dialog opened from the keyboard takes focus, and Escape gives it back
// to the composer.
await evaljs(`document.getElementById('input').focus(); 1`);
await cmd('Input.dispatchKeyEvent', { type: 'keyDown', key: 'k', code: 'KeyK', modifiers: 2, windowsVirtualKeyCode: 75 });
await cmd('Input.dispatchKeyEvent', { type: 'keyUp', key: 'k', code: 'KeyK', modifiers: 2, windowsVirtualKeyCode: 75 });
await sleep(200);
const palOpen = await evaljs(`!document.getElementById('palette').hidden && document.activeElement.id === 'palette-q'`);
await cmd('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
await cmd('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
await sleep(200);
const palBack = await evaljs(`document.getElementById('palette').hidden && document.activeElement.id === 'input'`);
check('Ctrl+K opens the palette with focus in it; Escape returns to the composer', palOpen && palBack, JSON.stringify({ palOpen, palBack }));

// An approval is announced and takes focus — to the card, never to
// Allow, so a stray Enter approves nothing — unless the user is typing.
const perm = (summary) => evaljs(`(() => { window.__es.onmessage({ data: JSON.stringify({ type: 'permission', permission: { name: 'bash', summary: '${summary}', preview: '${summary}' } }) }); return 1; })()`);
await evaljs(`(() => { const t = document.getElementById('input'); t.value = ''; t.focus(); return 1; })()`);
await perm('make test');
await sleep(300);
const askFocus = await evaljs(`({ on: document.activeElement.id, said: document.getElementById('sr-announce')?.textContent || '' })`);
check('an approval is announced and takes focus to the card, not to Allow', askFocus.on === 'ask' && /Approval needed/.test(askFocus.said) && /make test/.test(askFocus.said), JSON.stringify(askFocus));
await evaljs(`document.getElementById('ask').hidden = true; 1`);
await evaljs(`(() => { const t = document.getElementById('input'); t.value = 'half a thought'; t.focus(); return 1; })()`);
await perm('make lint');
await sleep(300);
check('…but not away from a half-typed message', await evaljs(`document.activeElement.id === 'input' && document.getElementById('input').value === 'half a thought'`));
await evaljs(`document.getElementById('ask').hidden = true; document.getElementById('input').value = ''; 1`);

// A finished turn is announced once, not token by token.
await evaljs(`(() => { const es = window.__es; es.onmessage({ data: JSON.stringify({ type: 'status', status: { running: true, phase: 'Writing', mode: 'ask' } }) }); es.onmessage({ data: JSON.stringify({ type: 'status', status: { running: false, mode: 'ask', status: 'done in 4s' } }) }); return 1; })()`);
await sleep(300);
const doneSaid = await evaljs(`document.getElementById('sr-announce')?.textContent || ''`);
check('a finished turn is announced', /done in 4s/.test(doneSaid), JSON.stringify(doneSaid));

console.log(results.join('\n'));
const failed = results.filter(r => r.startsWith('FAIL'));
console.log(failed.length ? `${failed.length} FAILURES` : 'ALL PASS');
ws.close();
process.exit(failed.length ? 1 : 0);
