// The page framed cross-site, as an editor's webview frames it
// (editors/vscode): a parent page on another site holds an iframe of
// janetcode's page with ?token=…&embed=1. Asserts, in real Chrome, that
// the frame gets past the token (the embed cookie is kept and sent from a
// cross-site frame), that the API answers inside it, and — when the
// server's language is not English — that the chrome is translated. With
// VOICE=1 and Chrome started with a fake microphone playing a speech
// file (--use-file-for-fake-audio-capture), it presses the voice button
// twice and waits for the transcript in the input.
//
// run: node embed.mjs <parent-url> [expected-placeholder]
// Requires chrome with --remote-debugging-port=9223.
const CDP_PORT = Number(process.env.UI_CDP_PORT || 9223);
const PARENT = process.argv[2];
const WANT_PLACEHOLDER = process.argv[3] || '';
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

async function getWsUrl() {
  for (let i = 0; i < 40; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?${PARENT}`, { method: 'PUT' });
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

let failed = 0;
const check = (ok, what) => { console.log((ok ? 'ok   ' : 'FAIL ') + what); if (!ok) failed++; };

await cmd('Page.enable');
await sleep(3000);
const tree = (await cmd('Page.getFrameTree')).result.frameTree;
const child = (tree.childFrames || [])[0];
check(!!child, 'the page is framed: ' + JSON.stringify(tree.frame.url));
if (!child) process.exit(1);
check(!new URL(child.frame.url).search.includes('token'), 'the frame traded its token and dropped it from the URL: ' + child.frame.url);
const world = (await cmd('Page.createIsolatedWorld', { frameId: child.frame.id, worldName: 'embedtest' })).result.executionContextId;
const inFrame = async (expression) => {
  const r = await cmd('Runtime.evaluate', { expression, contextId: world, returnByValue: true, awaitPromise: true });
  return r.result.result.value;
};
check(await inFrame('!!document.getElementById("input")'), 'the frame shows the app, not the sign-in page');
check(await inFrame('fetch("/api/state").then(r => r.status)') === 200, 'the API answers from inside the cross-site frame');
if (WANT_PLACEHOLDER) {
  await sleep(500);
  const ph = await inFrame('document.getElementById("input").placeholder');
  check(ph === WANT_PLACEHOLDER, `the composer placeholder is translated: ${JSON.stringify(ph)}`);
  check(await inFrame('document.documentElement.lang') !== 'en', 'the page says its language');
}
if (process.env.VOICE === '1') {
  check(await inFrame('!document.getElementById("voice-btn").hidden'), 'the voice button is shown');
  await inFrame('document.getElementById("voice-btn").click(), true');
  await sleep(500);
  check(await inFrame('document.getElementById("voice-btn").getAttribute("aria-pressed")') === 'true', 'recording: the button is pressed');
  await sleep(5000);
  await inFrame('document.getElementById("voice-btn").click(), true');
  let text = '';
  for (let i = 0; i < 60 && !text; i++) {
    await sleep(500);
    text = await inFrame('document.getElementById("input").value');
  }
  console.log('transcript: ' + JSON.stringify(text));
  check(/unit test/i.test(text), 'the recording was transcribed into the input');
}
ws.close();
process.exit(failed ? 1 : 0);
