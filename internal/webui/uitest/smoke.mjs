// Console-error smoke test: opens every view of the web UI in headless
// Chrome against a real `janetcode --web` and a fake OpenAI-compatible
// model, and fails on anything a user would hit as a broken page: a
// console error, an uncaught exception or unhandled promise rejection, a
// failed same-origin request (4xx/5xx or a network error), or a view whose
// key element (data-testid) is missing. It exists because a TDZ
// ReferenceError in dashboard.js once shipped with every other test green:
// nothing executed the page's scripts in that state.
//
// The fixture: two chats (one stays stored, one is the live session), a
// background run with a pending question (a copy of a stored session with
// a status sidecar naming this process as its pid), and one armed loop.
//
// usage: node smoke.mjs <appURL> <cdpPort> <modelPort> <sessionsDir>
import http from 'http';
import { copyFileSync, readFileSync, readdirSync, writeFileSync } from 'fs';
import { join } from 'path';

const [APP, CDP_PORT, MODEL_PORT, SESSIONS] = process.argv.slice(2);
const ORIGIN = new URL(APP).origin;
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// ---- the fake model: answers "ok" to everything
const model = http.createServer((req, res) => {
  if (req.url === '/v1/models') { res.writeHead(200, { 'content-type': 'application/json' }); res.end('{"data":[{"id":"smoke-model"}]}'); return; }
  if (req.url !== '/v1/chat/completions') { res.writeHead(404); res.end(); return; }
  req.resume();
  req.on('end', () => {
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.write(`data: ${JSON.stringify({ model: 'smoke-model', choices: [{ index: 0, delta: { role: 'assistant', content: 'ok' }, finish_reason: null }] })}\n\n`);
    res.write(`data: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] })}\n\n`);
    res.write(`data: ${JSON.stringify({ choices: [], usage: { prompt_tokens: 50, completion_tokens: 1 } })}\n\n`);
    res.end('data: [DONE]\n\n');
  });
});
await new Promise(r => model.listen(Number(MODEL_PORT), '127.0.0.1', r));

// ---- CDP
async function target() {
  for (let i = 0; i < 40; i++) {
    try { return await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: 'PUT' })).json(); }
    catch (_) { await sleep(250); }
  }
  throw new Error('chrome not up');
}
const ws = new WebSocket((await target()).webSocketDebuggerUrl);
await new Promise(r => ws.onopen = r);
let seq = 0;
const pending = new Map();
const problems = [];
const requests = new Map();
const note = (kind, text) => problems.push(`${kind}: ${String(text).split('\n')[0].slice(0, 300)}`);
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return; }
  const p = m.params || {};
  switch (m.method) {
    case 'Runtime.exceptionThrown':
      note('exception', p.exceptionDetails.exception?.description || p.exceptionDetails.text); break;
    case 'Runtime.consoleAPICalled':
      if (p.type === 'error' || p.type === 'assert') note('console.' + p.type, p.args.map(a => a.value ?? a.description ?? '').join(' ')); break;
    case 'Network.requestWillBeSent': requests.set(p.requestId, p.request.url); break;
    case 'Network.responseReceived':
      if (p.response.url.startsWith(ORIGIN) && p.response.status >= 400) note('http ' + p.response.status, p.response.url); break;
    case 'Network.loadingFailed': {
      const url = requests.get(p.requestId) || '';
      // An event stream or poll dropped by a reload or by the page going
      // away is not a failure of the page.
      if (url.startsWith(ORIGIN) && !p.canceled && !/ERR_ABORTED/.test(p.errorText)) note('request failed', `${p.errorText} ${url}`);
      break;
    }
  }
};
const cmd = (method, params = {}) => new Promise(res => { const id = ++seq; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
await cmd('Runtime.enable');
await cmd('Network.enable');
await cmd('Page.enable');
const js = async (expression) => {
  const r = await cmd('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (r.result.exceptionDetails) throw new Error('page JS error: ' + (r.result.exceptionDetails.exception?.description || r.result.exceptionDetails.text));
  return r.result.result.value;
};
const until = async (expr, ms = 10000) => {
  for (const end = Date.now() + ms; Date.now() < end; await sleep(100)) {
    try { if (await js(expr)) return true; } catch (_) { /* the page is reloading */ }
  }
  return false;
};
// shown: the element exists and takes room on the page.
const shown = (tid) => `(() => { const e = document.querySelector('[data-testid="${tid}"]'); return !!e && !e.hidden && e.getClientRects().length > 0; })()`;
const results = [];
const check = (name, ok, extra = '') => results.push(`${ok ? 'PASS' : 'FAIL'} ${name}${extra ? ' — ' + extra : ''}`);
// view: wait for each key element of a named view; the failure names it.
const view = async (name, ...tids) => {
  for (const t of tids) check(`${name}: ${t}`, await until(shown(t), 8000));
};
const send = (text) => js(`(() => { const i = document.getElementById('input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', {bubbles:true})); document.getElementById('send').click(); })()`);
const idle = () => until(`!document.body.classList.contains('busy') && document.getElementById('stop').hidden`, 15000);
// A script error that stops the run still says what the page logged first:
// a broken script is usually why the next control is missing.
process.on('uncaughtException', (e) => {
  for (const r of results) console.log(r);
  console.log(`FAIL the run stopped: ${e.message.split('\n')[0]}`);
  console.log(`page problems: ${problems.join(' | ') || 'none'}`);
  process.exit(1);
});
const click = (sel) => js(`document.querySelector(${JSON.stringify(sel)}).click()`);
const key = (k, extra = {}) => js(`document.dispatchEvent(new KeyboardEvent('keydown', Object.assign({ key: ${JSON.stringify(k)}, bubbles: true }, ${JSON.stringify(extra)})))`);

await cmd('Page.navigate', { url: APP });
await until(`document.readyState === 'complete' && !!document.getElementById('log')`, 20000);
await sleep(500);

// 1. main chat: two conversations; the first becomes a stored session.
await view('chat', 'chat-log', 'composer', 'sessions-list');
await send('first chat');
check('chat: the model answers', await until(`document.getElementById('log').innerText.includes('ok')`, 15000));
await idle();
await click('#new-chat');
await until(`!document.querySelector('#log article.turn')`);
await send('second chat');
await until(`document.getElementById('log').innerText.includes('ok')`, 15000);
await idle();
await click('#new-chat');
await until(`!document.querySelector('#log article.turn')`);
await send('third chat');
await until(`document.getElementById('log').innerText.includes('ok')`, 15000);
await idle();

// Fixture: a background run holding a question, from a stored session's
// file (new id and title). This process counts as the live run.
const stored = readdirSync(SESSIONS).filter(f => f.endsWith('.jsonl')).sort();
check('fixture: sessions on disk', stored.length >= 3, stored.join(','));
const src = join(SESSIONS, stored[0]);
const bgID = '20260101-000000-smoke1';
const lines = readFileSync(src, 'utf8').split('\n');
lines[0] = lines[0].replace(/"id":"[^"]*"/, `"id":"${bgID}"`).replace(/"title":"[^"]*"/, '"title":"background smoke run"');
writeFileSync(join(SESSIONS, bgID + '.jsonl'), lines.join('\n'));
writeFileSync(join(SESSIONS, bgID + '.bg.json'), JSON.stringify({
  state: 'running', pid: process.pid, question: { id: 'q1', items: [{ prompt: 'Which database should the migration use?' }] }, started: new Date().toISOString(),
  pending_question: { text: 'Which database should the migration use?', choices: ['postgres', 'sqlite'], asked: new Date().toISOString(), about: 'question' },
}));

// A loop: armed through the same command line the composer takes.
const armed = await js(`fetch('/api/command', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ text: '/loop 1h smoke check' }) }).then(r => r.status)`);
check('loop: /loop armed', armed === 200, String(armed));

// 2. sessions sidebar: the stored chats and the background run are listed.
await js(`window.dispatchEvent(new Event('janet:sessions-refresh'))`);
check('sessions: stored chats listed', await until(`document.querySelectorAll('#sessions li[data-id]').length >= 3`, 8000));

// 3. dashboard: live + stored + background-with-question, and the loop.
await click('#dash-btn');
await view('dashboard', 'dashboard', 'dash-pending', 'dash-pending-reply', 'dash-loops');
check('dashboard: live, stored and background cards', await until(`(() => { const t = document.getElementById('dash-body').innerText; return t.includes('this window') && t.includes('stored') && t.includes('background'); })()`));
check('dashboard: the loop is listed', await until(`!!document.querySelector('[data-testid="dash-loops"] .dash-loop')`));
check('dashboard: no Approve unless the mandate changed', await js(`!document.querySelector('[data-testid="loop-approve"]')`));
await click('[data-testid="loop-mode"]');
check('dashboard: Observe turns the loop to observe, offering Run', await until(`document.querySelector('[data-testid="loop-mode"]')?.textContent.trim() === 'Run'`, 8000));
await click('[data-testid="loop-mode"]');
check('dashboard: Run puts it back, offering Observe', await until(`document.querySelector('[data-testid="loop-mode"]')?.textContent.trim() === 'Observe'`, 8000));
await click('#dash-close');
check('dashboard: closes', await until(`document.getElementById('dashboard').hidden`));

// 4. composer mode menu and Away.
await click('#mode-chip');
await view('mode menu', 'mode-opt-away');
await click('[data-testid="mode-opt-away"]');
check('away: on, the chip says Away', await until(`document.getElementById('mode-chip').dataset.mode === 'AWAY'`, 8000));
await click('#work-toggle');
await click('[data-pane="session"]');
await view('session panel', 'away-toggle');
await until(shown('away-toggle'));
check('away: the session panel shows it on', await until(`document.getElementById('mode-away').classList.contains('on')`, 8000));
await click('#mode-away');
check('away: off again', await until(`document.getElementById('mode-chip').dataset.mode !== 'AWAY'`, 8000));
await click('#work-toggle');

// 5. palette, export dialog, settings, help.
await key('k', { ctrlKey: true });
await view('palette', 'palette');
await js(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))`);
check('palette: closes', await until(`document.getElementById('palette').hidden`));
await click('#export-btn');
await view('export', 'dialog-exp');
await click('.xdlg .xd-close');
check('export: closes', await until(`!document.querySelector('.xdlg')`));
await click('#settings-btn');
await view('settings', 'settings');
await click('#settings-close');
await click('#help-btn');
await view('help', 'help');
await key('Escape');

await sleep(500); // late rejections and failed polls
check('no console errors, exceptions, unhandled rejections or failed requests', problems.length === 0, problems.join(' | '));
ws.close();
model.close();
for (const r of results) console.log(r);
const failed = results.filter(r => r.startsWith('FAIL')).length;
console.log(failed ? `${failed} FAILED` : 'ALL PASS');
process.exit(failed ? 1 : 0);
