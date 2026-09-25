// First run in the browser, end to end: janetcode --web started with no
// config and no key must open on the connect form, say plainly when a key
// is rejected, and connect with a good one — switching the header and
// writing the config file — without the user touching JSON.
//
// The provider is a tiny OpenAI-compatible server started here: its model
// list answers only to "Bearer good-key", like a real provider's 401.
//
// usage: node firstrun.mjs <appURL> <cdpPort> <modelPort> <configFile>
import http from 'http';
import { readFileSync } from 'fs';

const [APP, CDP_PORT, MODEL_PORT, CONFIG] = process.argv.slice(2);
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

const models = http.createServer((req, res) => {
  if (req.url !== '/v1/models') { res.writeHead(404); res.end(); return; }
  if (req.headers.authorization !== 'Bearer good-key') {
    res.writeHead(401, { 'content-type': 'application/json' });
    res.end('{"error":{"message":"Incorrect API key provided"}}');
    return;
  }
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end('{"data":[{"id":"house-coder"},{"id":"house-mini"}]}');
});
await new Promise(r => models.listen(Number(MODEL_PORT), '127.0.0.1', r));

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
const until = async (expr, ms = 8000) => {
  for (const end = Date.now() + ms; Date.now() < end; await sleep(100)) if (await js(expr)) return true;
  return false;
};
const results = [];
const check = (name, ok, extra = '') => results.push(`${ok ? 'PASS' : 'FAIL'} ${name}${extra ? ' — ' + extra : ''}`);
const $ = (id) => `document.getElementById(${JSON.stringify(id)})`;
const setVal = (id, v) => js(`(() => { const e = ${$(id)}; e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('input', {bubbles:true})); e.dispatchEvent(new Event('change', {bubbles:true})); })()`);

await sleep(1500);
const ready = () => until(`!${$('connect')}.hidden && ${$('connect')}.dataset.ready === '1'`);
check('the page opens on the connect form', await ready());
check('the header says it is not connected', await js(`${$('model')}.textContent.trim() === 'Not connected'`), await js(`${$('model')}.textContent`));
check('focus starts in the form', await js(`${$('connect')}.contains(document.activeElement)`));

// A prompt typed before connecting is kept, not lost.
await js(`${$('connect-form')}.querySelector('#cx-cancel').click()`);
await setVal('input', 'add a --json flag');
await js(`document.getElementById('send').click()`);
check('a prompt before connecting reopens the form', await ready());
check('…and stays in the composer', await js(`${$('input')}.value === 'add a --json flag'`));

await setVal('cx-provider', 'custom-openai');
check('a custom server asks for a name', await js(`!${$('cx-custom')}.hidden`));
await setVal('cx-name', 'house');
await setVal('cx-url', `http://127.0.0.1:${MODEL_PORT}/v1`);
await setVal('cx-key', 'wrong-key');
await js(`${$('cx-list')}.click()`);
await until(`${$('cx-msg')}.classList.contains('bad')`);
const bad = await js(`${$('cx-msg')}.textContent`);
check('a rejected key says so, before any prompt', /Authentication failed/.test(bad) && /API key for house above/.test(bad), bad);
check('…and marks the key field', await js(`${$('cx-key')}.getAttribute('aria-invalid') === 'true'`));
check('…the message is announced', await js(`${$('cx-msg')}.getAttribute('aria-live') === 'polite'`));

await setVal('cx-key', 'good-key');
// Enter with no model chosen lists the models.
await js(`${$('connect-form')}.requestSubmit()`);
await until(`${$('cx-msg')}.classList.contains('ok')`);
const listed = await js(`[...${$('cx-models')}.options].map(o => o.value).join(',')`);
check('a good key lists the models', listed === 'house-coder,house-mini', listed);
check('…and clears the error mark', await js(`!${$('cx-key')}.hasAttribute('aria-invalid')`));

await setVal('cx-model', 'house-coder');
await js(`${$('connect-form')}.requestSubmit()`);
check('connecting closes the form', await until(`${$('connect')}.hidden`));
const head = await js(`${$('model')}.textContent`);
check('the header names the new model', /house/.test(head) && /house-coder/.test(head), head);
check('the status says where it was saved', /saved to/.test(await js(`${$('status')}.textContent`)), await js(`${$('status')}.textContent`));

let saved = {};
try { saved = JSON.parse(readFileSync(CONFIG, 'utf8')); } catch (e) { saved = { error: String(e) }; }
const p = saved.providers?.house || {};
check('the config file holds the choice', saved.provider === 'house' && saved.model === 'house-coder' && p.api_key === 'good-key' && p.type === 'openai',
  JSON.stringify({ provider: saved.provider, model: saved.model, type: p.type, key: p.api_key ? 'set' : 'missing' }));

// The key never comes back to the page.
const list = await js(`fetch('/api/connect').then(r => r.text())`);
check('the provider list does not send the key back', !list.includes('good-key') && list.includes('"has_key":true'));

check('no uncaught exceptions in the page', pageErrors.length === 0, pageErrors.join(' | '));
ws.close();
models.close();
for (const r of results) console.log(r);
const failed = results.filter(r => r.startsWith('FAIL')).length;
console.log(failed ? `${failed} FAILED` : 'ALL PASS');
process.exit(failed ? 1 : 0);
