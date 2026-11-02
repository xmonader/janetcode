// Screenshot harness: boots the webui against a stub provider, feeds
// synthetic SSE fixtures through a patched EventSource, and captures PNGs
// of the states a user actually sees. Run: node shot.mjs <url> <outdir>
// Requires a chrome with --remote-debugging-port=9223 (see make ui-shot).
const APP = process.argv[2] || 'http://127.0.0.1:18991/';
const OUT = process.argv[3] || '/tmp/ui-shots';
const CDP_PORT = Number(process.argv[4] || 9223);
import { mkdirSync, writeFileSync } from 'fs';
mkdirSync(OUT, { recursive: true });
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

async function newTab() {
  for (let i = 0; i < 40; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: 'PUT' });
      return (await r.json()).webSocketDebuggerUrl;
    } catch (_) { await sleep(250); }
  }
  throw new Error('chrome not up');
}

const ws = new WebSocket(await newTab());
await new Promise(r => ws.onopen = r);
let seq = 0; const pending = new Map();
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } };
const cmd = (method, params = {}) => new Promise((res) => { const id = ++seq; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
const evaljs = async (expression) => {
  const r = await cmd('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
  if (r.result.exceptionDetails) throw new Error(JSON.stringify(r.result.exceptionDetails).slice(0, 400));
  return r.result.result.value;
};

// Before app.js loads, swap EventSource for a fake one we can feed.
await cmd('Page.enable');
await cmd('Page.addScriptToEvaluateOnNewDocument', { source: `
  window.__feed = [];
  // Dark from the first paint, the way a returning dark-mode user loads it.
  localStorage.setItem('janetcode-theme', 'dark');
  window.EventSource = class {
    constructor(url) { this.url = url; window.__es = this; setTimeout(() => this.onopen && this.onopen(), 0); }
    close() {}
  };
  // The empty page's chips read the repository the server runs in; pin
  // them so the shots do not follow the working tree.
  const realFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => String(url).startsWith('/api/suggest')
    ? Promise.resolve(new Response(JSON.stringify([
        { label: 'Review my 3 changed files', prompt: 'Review my uncommitted changes' },
        { label: 'Resolve a TODO in parser.go', prompt: 'Resolve the TODO at inifile/parser.go:88' },
        { label: 'Check the last commit', prompt: 'Explain what commit 1a2b3c4 changed' },
        { label: 'Add tests to inifile', prompt: 'The Go package in internal/inifile has no tests' },
      ]), { headers: { 'Content-Type': 'application/json' } }))
    : String(url).startsWith('/api/workspace') && !opts
    // The branch and the context share come from the tree and the model
    // this run happens to have; pin them so the pixels do not follow it.
    ? realFetch(url, opts).then(async (r) => { const d = await r.clone().json(); d.branch = 'main'; d.ctx_pct = 34; d.ctx_label = '68k / 200k'; return new Response(JSON.stringify(d), { headers: { 'Content-Type': 'application/json' } }); })
    : realFetch(url, opts);
`});
await cmd('Page.navigate', { url: APP });
await sleep(2500);
await cmd('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
await sleep(300);
await cmd('Page.captureScreenshot', { format: 'png' }).then(r => { writeFileSync(`${OUT}/00-empty.png`, Buffer.from(r.result.data, 'base64')); console.log('shot 00-empty'); });

const GO = 'func Interpolate(v string, seen map[string]bool) (string, error) {\n\tif !strings.Contains(v, "%(") {\n\t\treturn v, nil\n\t}\n\treturn v, nil\n}';
const ASSISTANT = [
  'Here is the interpolation fix. The `INI.Parser` now resolves `%(key)s` references with cycle detection:',
  '',
  '```go',
  GO,
  '```',
  '',
  '**What changed:**',
  '',
  '1. `Interpolate` takes a `seen` set so `a=%(b)s / b=%(a)s` raises `ParseError` instead of looping',
  '2. defaults fall back through the [repository map](https://example.com/docs)',
  '',
  '| stage | before | after |',
  '|---|---|---|',
  '| lookups | O(n²) | O(n) |',
  '| cycles | loop | ParseError |',
  '',
  'Verification: `go test ./...` passes.',
].join('\n');

const FEED = [
  { type: 'hello', hello: { cwd: '/home/xmonader/wspace/janetcode', model: 'qwen3.7-plus', provider: 'openrouter', session: 's1', title: 'Fix INI interpolation', version: 'dev', yolo: false, auto: false, edits: false, plan: false, readonly: false, mode: 'ask' } },
  { type: 'entry', entry: { id: 'e1', kind: 'user', text: 'Fix the interpolation in the INI parser so %(key)s references resolve, with cycle detection.', at: new Date().toISOString() } },
  { type: 'entry', entry: { id: 'e2', kind: 'thinking', text: 'The parser has an Interpolate method. Cycles need a seen set threaded through.', done: true } },
  { type: 'entry', entry: { id: 'e3', kind: 'tool', name: 'read_file', summary: 'internal/inifile/parser.go', done: true, dur: '0.4s', output: 'func (p *Parser) Interpolate(v string) string {\n    return v\n}' } },
  { type: 'entry', entry: { id: 'e4', kind: 'tool', name: 'edit_file', summary: 'internal/inifile/parser.go', preview: '@@ -120,3 +120,9 @@\n func (p *Parser) Interpolate(v string) string {\n-    return v\n+    return p.interpolate(v, "_DEFAULT", make(map[string]bool))\n+}\n+\n+func (p *Parser) interpolate(v, section string, seen map[string]bool) (string, error) {\n+    if seen == nil {\n+        seen = make(map[string]bool)\n+    }', done: true, dur: '1.2s' } },
  { type: 'entry', entry: { id: 'e5', kind: 'tool', name: 'bash', summary: 'go test ./internal/inifile/', done: true, dur: '6.1s', output: 'ok  \tgithub.com/xmonader/janetcode/internal/inifile\t0.312s' } },
  { type: 'entry', entry: { id: 'e6', kind: 'assistant', text: ASSISTANT, done: true, tok_in: 12034, tok_out: 812, ms: 21400, at: '2026-01-15T14:52:00Z' } },
  { type: 'entry', entry: { id: 'e7', kind: 'verdict', text: '✓ tests passed', tone: 'ok' } },
  { type: 'status', status: { running: false, phase: '', ctx_pct: 34, queued: 0, yolo: false, auto: false, edits: false, plan: false, readonly: false, mode: 'ask', verbose: false, status: 'done in 21s', cost: '$0.0042' } },
];

async function shot(name) {
  const r = await cmd('Page.captureScreenshot', { format: 'png' });
  writeFileSync(`${OUT}/${name}.png`, Buffer.from(r.result.data, 'base64'));
  console.log('shot', name);
}
// Through the page's own toggle, so every piece of chrome that follows
// the theme (its icon, the settings segment) is drawn as a user sees it.
const setTheme = (t) => evaljs(`(() => { if (document.documentElement.dataset.theme !== '${t}') document.getElementById('theme-toggle').click(); return document.documentElement.dataset.theme; })()`);
const setSize = async (w, h) => { await cmd('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: w < 700 }); await sleep(350); };

// The message clock is a real time; pin the zone it is read in so the
// shot is the same on any machine.
await cmd('Emulation.setTimezoneOverride', { timezoneId: 'UTC' });

// A spinner caught mid-turn, or a fade half done, is a different picture
// every run; the gate compares still frames.
await evaljs(`(() => { const st = document.createElement('style'); st.textContent = '*, *::before, *::after { animation: none !important; transition: none !important; caret-color: transparent !important; }'; document.head.appendChild(st); return 1; })()`);

// feed fixtures through the fake EventSource
await evaljs(`(() => {
  const es = window.__es;
  const feed = ${JSON.stringify(FEED)};
  // The page compares the hello's build against the one it was served
  // with; match it so the stale-UI toast stays out of the shots.
  feed[0].hello.version = window.__janetUIBuild;
  for (const env of feed) es.onmessage({ data: JSON.stringify(env) });
  return feed.length;
})()`);
await sleep(600);

await setSize(1440, 900);
await setTheme('dark');
await shot('01-chat-dark');
await evaljs(`document.getElementById('chat-view').scrollTop = 0; 1`);
await sleep(300);
await shot('01b-chat-top');
// A command opened: its output is a terminal card.
await evaljs(`(() => { const h = document.querySelector('.tool.tk-cmd .head'); h.click(); return 1; })()`);
await sleep(300);
await evaljs(`document.getElementById('chat-view').scrollTop = 120; 1`);
await sleep(200);
await shot('01c-command-open');
await evaljs(`(() => { const h = document.querySelector('.tool.tk-cmd .head'); h.click(); return 1; })()`);
await sleep(200);
await evaljs(`(() => { const v = document.getElementById('chat-view'); v.scrollTop = v.scrollHeight; return 1; })()`);

// permission card
await evaljs(`document.getElementById('work-toggle').click()`);
await sleep(450);
await shot('02-drawer-open');

// light theme
await setTheme('light');
await sleep(250);
await shot('03-chat-light');
await setTheme('dark');

// running turn with a permission card up
await evaljs(`(() => {
  const es = window.__es;
  const send = (env) => es.onmessage({ data: JSON.stringify(env) });
  send({ type: 'status', status: { running: true, phase: 'tool', ctx_pct: 36, mode: 'ask', status: 'running bash' } });
  send({ type: 'entry', entry: { id: 'e8', kind: 'tool', name: 'bash', summary: 'rm -rf build && make', done: false } });
  send({ type: 'permission', permission: { name: 'bash', summary: 'rm -rf build && make', rule: 'bash(rm -rf build*)', impact: 'deletes build/ (214 files)', preview: 'rm -rf build && make' } });
  return 1;
})()`);
await sleep(500);
await shot('05-permission');
await evaljs(`(() => { document.getElementById('ask').hidden = true; window.__es.onmessage({ data: JSON.stringify({ type: 'status', status: { running: false, mode: 'ask', ctx_pct: 36, status: 'done in 21s' } }) }); return 1; })()`);
await sleep(300);

// sheets and views: settings, help, palette, trajectory
const hideAll = `['settings','help','palette'].forEach(id => document.getElementById(id).hidden = true)`;
await evaljs(`document.getElementById('settings-btn').click(); 1`);
await sleep(300);
await shot('06-settings');
await evaljs(`${hideAll}; document.getElementById('help-btn').click(); 1`);
await sleep(300);
await shot('07-help');
await evaljs(`${hideAll}; document.getElementById('search-btn').click(); 1`);
await sleep(300);
await shot('08-palette');
await evaljs(`${hideAll}; document.querySelector('[data-view=trajectory]').click(); 1`);
await sleep(500);
await shot('09-trajectory');
await evaljs(`document.querySelector('[data-view=chat]').click(); 1`);
await sleep(300);

// mobile
// The pane, open since 02, is remembered: close it so the phone and the
// later states show the default layout.
await evaljs(`document.querySelector('.shell').classList.contains('work-collapsed') || document.getElementById('work-toggle').click()`);
await setSize(390, 844);
await sleep(400);
await shot('04a-mobile-chat');
await evaljs(`document.querySelector('.shell').classList.remove('nav-collapsed')`);
await sleep(400);
await shot('04-mobile-nav');

// The states a person meets besides a finished chat, each in both
// themes: a turn at work, an approval, a provider failure, the connect
// form, and a fresh conversation.
await setSize(1440, 900);
const feedJS = (envs) => evaljs(`(() => { for (const env of ${JSON.stringify(envs)}) window.__es.onmessage({ data: JSON.stringify(env) }); return 1; })()`);
const idle = { type: 'status', status: { running: false, mode: 'ask', ctx_pct: 36, status: 'done in 21s' } };
const closeAll = `(() => { ['settings','help','palette','connect','ask'].forEach(id => { const e = document.getElementById(id); if (e) e.hidden = true; }); return 1; })()`;
for (const theme of ['dark', 'light']) {
  await setTheme(theme);
  await evaljs(closeAll);
  await feedJS([
    { type: 'status', status: { running: true, phase: 'Noodling…', elapsed: '12s', ctx_pct: 38, mode: 'ask' } },
    // A folded burst with one failure: only its "(1 failed)" is red.
    { type: 'entry', entry: { id: 'rb1', kind: 'tool', name: 'bash', summary: 'go vet ./...', done: true, run: 'rb1', run_head: true, run_label: 'Ran 2 commands (1 failed)' } },
    { type: 'entry', entry: { id: 'rb2', kind: 'tool', name: 'bash', summary: 'go test ./config', done: true, error: true, run: 'rb1', run_label: 'Ran 2 commands (1 failed)' } },
    { type: 'entry', entry: { id: 'run-th', kind: 'thinking', text: 'The interpolation has to see the section it runs in, so the seen-set is per call, not per parser. A cycle comes back as an error rather than a stack overflow.', done: false } },
  ]);
  await sleep(500);
  await shot(`10-running-${theme}`);
  await feedJS([
    { type: 'entry', entry: { id: 'run-th', kind: 'thinking', text: 'The interpolation has to see the section it runs in.', done: true } },
    { type: 'entry', entry: { id: 'e8', kind: 'tool', name: 'bash', summary: 'rm -rf build && make', done: false } },
    { type: 'permission', permission: { name: 'bash', summary: 'rm -rf build && make', rule: 'bash(rm -rf build*)', impact: 'deletes build/ (214 files)', preview: 'rm -rf build && make' } },
  ]);
  await sleep(500);
  await shot(`11-permission-${theme}`);
  await evaljs(closeAll);
  await feedJS([
    { type: 'entry', entry: { id: 'e8', kind: 'tool', name: 'bash', summary: 'rm -rf build && make', done: true, error: true, denied: true } },
    { type: 'entry', entry: { id: 'err1', kind: 'error', error: true, text: 'Authentication failed (HTTP 401): Unauthorized. Check the API key for stub with /connect.',
      data: { headline: 'Authentication failed', next: 'Check the API key for stub with /connect.', code: 'HTTP 401', reason: 'Unauthorized', retry: false } } },
    idle,
  ]);
  await sleep(500);
  await shot(`12-error-${theme}`);
  await feedJS([{ type: 'connect' }]);
  await evaljs(`new Promise(r => { const t = setInterval(() => { if (document.getElementById('connect').dataset.ready) { clearInterval(t); r(1); } }, 50); })`);
  // The config lives in a fresh temp dir each run; pin the path shown.
  await evaljs(`document.getElementById('cx-path').textContent = '~/.config/janetcode/config.json'; 1`);
  await sleep(300);
  await shot(`13-connect-${theme}`);
  await evaljs(closeAll);
}
await setTheme('dark');
for (const theme of ['dark', 'light']) {
  await setTheme(theme);
  await feedJS([{ type: 'reset' }, { type: 'status', status: { running: false, mode: 'ask', ctx_pct: 0 } }]);
  await sleep(400);
  await shot(`14-empty-${theme}`);
}

ws.close();
console.log('done ->', OUT);
process.exit(0);
