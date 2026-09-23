// Visual regression gate: compares every screenshot shot.mjs took with the
// committed baseline of the same name, inside headless Chrome (a canvas
// does the pixel work, so there is nothing to install). A state that
// changed by more than the tolerance fails the run and leaves a diff
// image next to the new shot, red where the pixels moved.
//
// usage: node shotdiff.mjs <cdpPort> <newDir> <baselineDir> [--update]
// --update copies the new shots over the baselines instead of comparing.
import { readdirSync, readFileSync, writeFileSync, copyFileSync, existsSync, mkdirSync } from 'fs';

const [PORT, NEW, BASE] = process.argv.slice(2);
const UPDATE = process.argv.includes('--update');
// A pixel counts as changed when any channel moves by more than CHANNEL;
// a shot fails when more than MAXPIX pixels changed. The shots are still
// frames (shot.mjs stops animations, pins the clock's zone) and repeat to
// the pixel, so the bar is a count, not a share: at 0.2% a verdict badge
// that lost its colour passed, and at 0.01% a duration that went from
// "21s" to "21.4s" on a 1440x900 frame did.
const CHANNEL = 12;
const MAXPIX = 40;

const shots = readdirSync(NEW).filter(f => f.endsWith('.png') && !f.endsWith('.diff.png')).sort();
if (shots.length === 0) {
  console.error('shotdiff: no screenshots in ' + NEW);
  process.exit(1);
}
if (UPDATE) {
  mkdirSync(BASE, { recursive: true });
  for (const f of shots) copyFileSync(`${NEW}/${f}`, `${BASE}/${f}`);
  console.log(`shotdiff: ${shots.length} baselines updated in ${BASE}`);
  process.exit(0);
}

const target = await (await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })).json();
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise(r => ws.onopen = r);
let seq = 0;
const pending = new Map();
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } };
const cmd = (method, params = {}) => new Promise(res => { const id = ++seq; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });

// compare runs in the page: decode both PNGs, count changed pixels, and
// paint a diff image (the new shot faded, changed pixels in red).
const compare = `async (a, b, channel) => {
  const load = (src) => new Promise((ok, bad) => { const i = new Image(); i.onload = () => ok(i); i.onerror = bad; i.src = src; });
  const [ia, ib] = await Promise.all([load(a), load(b)]);
  if (ia.width !== ib.width || ia.height !== ib.height) return { size: [ia.width, ia.height, ib.width, ib.height] };
  const c = document.createElement('canvas'); c.width = ia.width; c.height = ia.height;
  const x = c.getContext('2d');
  x.drawImage(ia, 0, 0); const da = x.getImageData(0, 0, c.width, c.height).data;
  x.clearRect(0, 0, c.width, c.height);
  x.drawImage(ib, 0, 0); const img = x.getImageData(0, 0, c.width, c.height); const db = img.data;
  let changed = 0;
  for (let i = 0; i < da.length; i += 4) {
    const moved = Math.abs(da[i] - db[i]) > channel || Math.abs(da[i + 1] - db[i + 1]) > channel || Math.abs(da[i + 2] - db[i + 2]) > channel;
    if (moved) { changed++; db[i] = 255; db[i + 1] = 0; db[i + 2] = 0; db[i + 3] = 255; }
    else { db[i + 3] = 60; }
  }
  x.putImageData(img, 0, 0);
  return { changed, total: da.length / 4, diff: c.toDataURL('image/png') };
}`;

const url = (p) => 'data:image/png;base64,' + readFileSync(p).toString('base64');
let failed = 0;
for (const f of shots) {
  const base = `${BASE}/${f}`;
  if (!existsSync(base)) {
    console.log(`NEW  ${f} — no baseline; run make ui-visual UPDATE=1 to accept it`);
    failed++;
    continue;
  }
  const r = await cmd('Runtime.evaluate', {
    expression: `(${compare})(${JSON.stringify(url(base))}, ${JSON.stringify(url(`${NEW}/${f}`))}, ${CHANNEL})`,
    awaitPromise: true, returnByValue: true,
  });
  const v = r.result?.result?.value;
  if (!v) {
    console.log(`FAIL ${f} — could not compare: ${JSON.stringify(r.result?.exceptionDetails?.text || r.result)}`);
    failed++;
    continue;
  }
  if (v.size) {
    console.log(`FAIL ${f} — size changed ${v.size[0]}x${v.size[1]} → ${v.size[2]}x${v.size[3]}`);
    failed++;
    continue;
  }
  const ratio = v.changed / v.total;
  if (v.changed > MAXPIX) {
    const out = f.replace(/\.png$/, '.diff.png');
    writeFileSync(`${NEW}/${out}`, Buffer.from(v.diff.split(',')[1], 'base64'));
    console.log(`FAIL ${f} — ${v.changed} pixels changed, ${(ratio * 100).toFixed(3)}% (${out})`);
    failed++;
  } else {
    console.log(`PASS ${f} — ${(ratio * 100).toFixed(3)}% changed`);
  }
}
ws.close();
console.log(failed ? `${failed} VISUAL CHANGES` : `ALL ${shots.length} MATCH`);
process.exit(failed ? 1 : 0);
