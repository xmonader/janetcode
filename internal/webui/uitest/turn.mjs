// Real turns through the real server: janetcode --web talks to a scripted
// OpenAI-compatible model started here, and the page is driven in headless
// Chrome. Nothing is faked inside the page, so this covers what
// interact.mjs cannot: the model call, the permission round trip through
// /api/permission, the file really written, and the /events log replayed
// after a reload, including one in the middle of a streaming reply.
//
// The model answers by what it is sent:
//   "write <file>"  → a write_file call for <file>, then "Wrote <file>."
//                      once the result comes back (or "Not written." if
//                      the result is an error)
//   "drip"          → a reply streamed slowly, a word every 150 ms
//   anything else   → "ok"
//
// usage: node turn.mjs <appURL> <cdpPort> <modelPort> <workDir>
import http from 'http';
import { existsSync, readFileSync } from 'fs';
import { join } from 'path';

const [APP, CDP_PORT, MODEL_PORT, WORK] = process.argv.slice(2);
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

const DRIP = 'one two three four five six seven eight nine ten done dripping'.split(' ');
let calls = 0;

function chunk(res, delta, finish = null) {
  res.write(`data: ${JSON.stringify({ model: 'turn-model', choices: [{ index: 0, delta, finish_reason: finish }] })}\n\n`);
}

function finish(res, reason) {
  chunk(res, {}, reason);
  res.write(`data: ${JSON.stringify({ choices: [], usage: { prompt_tokens: 50, completion_tokens: 5 } })}\n\n`);
  res.end('data: [DONE]\n\n');
}

const model = http.createServer((req, res) => {
  if (req.url === '/v1/models') {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end('{"data":[{"id":"turn-model"}]}');
    return;
  }
  if (req.url !== '/v1/chat/completions') { res.writeHead(404); res.end(); return; }
  let body = '';
  req.on('data', (d) => { body += d; });
  req.on('end', async () => {
    calls++;
    const msgs = JSON.parse(body).messages || [];
    const last = msgs[msgs.length - 1] || {};
    const lastUser = [...msgs].reverse().find(m => m.role === 'user') || {};
    const said = typeof lastUser.content === 'string' ? lastUser.content
      : (lastUser.content || []).map(p => p.text || '').join(' ');
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    if (last.role === 'tool') {
      const file = (said.match(/write (\S+)/) || [])[1] || '?';
      const failed = /denied|not run|declined/i.test(String(last.content));
      chunk(res, { role: 'assistant', content: failed ? 'Not written.' : `Wrote ${file}.` });
      finish(res, 'stop');
      return;
    }
    const w = said.match(/write (\S+)/);
    if (w) {
      chunk(res, { role: 'assistant', tool_calls: [{ index: 0, id: 'call_' + calls, type: 'function', function: { name: 'write_file', arguments: JSON.stringify({ path: w[1], content: `hi from the model\n` }) } }] });
      finish(res, 'tool_calls');
      return;
    }
    if (/drip/.test(said)) {
      for (const word of DRIP) {
        chunk(res, { content: word + ' ' });
        await sleep(150);
      }
      finish(res, 'stop');
      return;
    }
    chunk(res, { role: 'assistant', content: 'ok' });
    finish(res, 'stop');
  });
});
await new Promise(r => model.listen(Number(MODEL_PORT), '127.0.0.1', r));

async function target() {
  for (let i = 0; i < 40; i++) {
    try { return await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?${APP}`, { method: 'PUT' })).json(); }
    catch (_) { await sleep(250); }
  }
  throw new Error('chrome not up');
}
const ws = new WebSocket((await target()).webSocketDebuggerUrl);
await new Promise(r => ws.onopen = r);
let seq = 0;
const pending = new Map();
// Uncaught exceptions anywhere in the page's scripts fail the run.
const pageErrors = [];
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
  if (m.method === 'Runtime.exceptionThrown') pageErrors.push(String(m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text).split('\n')[0]);
};
const cmd = (method, params = {}) => new Promise(res => { const id = ++seq; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
await cmd('Runtime.enable');
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
const results = [];
const check = (name, ok, extra = '') => results.push(`${ok ? 'PASS' : 'FAIL'} ${name}${extra ? ' — ' + extra : ''}`);
const logText = () => js(`document.getElementById('log').innerText`);
const send = (text) => js(`(() => { const i = document.getElementById('input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', {bubbles:true})); document.getElementById('send').click(); })()`);
const idle = () => until(`!document.body.classList.contains('busy') && !document.getElementById('cancel')?.offsetParent`, 15000);
const reload = async () => { await cmd('Page.reload', { ignoreCache: true }); await sleep(300); await until(`document.readyState === 'complete' && !!document.getElementById('log')`); };

await sleep(1500);

// 1. A write the user allows: the card asks about the file, the file is
// written by the real tool, and the model's answer arrives.
await send('write hello.txt');
check('a write asks for permission', await until(`!document.getElementById('ask').hidden`));
const asked = await js(`document.getElementById('ask-line').textContent`);
check('…naming the file', /hello\.txt/.test(asked), asked);
await js(`document.querySelector('#ask [data-d="allow"]').click()`);
check('the card goes away once answered', await until(`document.getElementById('ask').hidden`));
check('the model answers after the tool result', await until(`document.getElementById('log').innerText.includes('Wrote hello.txt.')`), (await logText()).slice(-200));
const path = join(WORK, 'hello.txt');
check('the file is on disk', existsSync(path) && readFileSync(path, 'utf8') === 'hi from the model\n');
check('the tool row says it is done', await js(`!!document.querySelector('#log .tool.st-done')`));

// 2. A reload shows the same conversation, from the server's log.
await reload();
check('after a reload the answer is still there', await until(`document.getElementById('log').innerText.includes('Wrote hello.txt.')`), (await logText()).slice(-200));
check('…and so is the tool row', await until(`!!document.querySelector('#log .tool.st-done')`));

// 3. A write the user denies: nothing is written, and the model is told.
await idle();
await send('write denied.txt');
check('a second write asks again', await until(`!document.getElementById('ask').hidden`));
await js(`document.querySelector('#ask [data-d="deny"]').click()`);
check('a denied write is reported to the model', await until(`document.getElementById('log').innerText.includes('Not written.')`), (await logText()).slice(-200));
check('…and the file does not exist', !existsSync(join(WORK, 'denied.txt')));
check('…and the row says it was declined', await js(`!!document.querySelector('#log .tool.st-declined')`));

// 4. A reload in the middle of a streaming reply: the page picks the turn
// up from the log and shows the whole reply once it ends.
await idle();
await send('drip');
await until(`document.getElementById('log').innerText.includes('three')`);
await reload();
check('a reload mid-reply shows the whole reply', await until(`document.getElementById('log').innerText.includes('done dripping')`, 15000), (await logText()).slice(-200));
const once = await js(`(document.getElementById('log').innerText.match(/one two three/g) || []).length`);
check('…exactly once', once === 1, `seen ${once} times`);

check('no uncaught exceptions in the page', pageErrors.length === 0, pageErrors.join(' | '));
ws.close();
model.close();
for (const r of results) console.log(r);
const failed = results.filter(r => r.startsWith('FAIL')).length;
console.log(failed ? `${failed} FAILED` : 'ALL PASS');
process.exit(failed ? 1 : 0);
