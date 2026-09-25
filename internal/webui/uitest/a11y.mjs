// WCAG contrast guard over the web palette (internal/webui/static/app.css).
// The TUI themes have TestBuiltinContrast; the web palette had nothing.
// Parses the :root and dark-theme token blocks, computes contrast ratios
// for every text/background pair the UI actually uses, and fails below
// threshold. Run: node a11y.mjs   (or make ui-a11y; part of make check)
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, '../static/app.css'), 'utf8');

function block(sel) {
  const m = css.match(new RegExp(sel.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*\\{([\\s\\S]*?)\\}'));
  return m ? m[1] : '';
}
function tokens(src) {
  const out = {};
  for (const m of src.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}
function rgba(str) {
  const m = /rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)/.exec(str);
  if (m) return [+m[1], +m[2], +m[3], m[4] == null ? 1 : +m[4]];
  const h = /^#([0-9a-f]{6})$/i.exec(str);
  if (h) {
    const v = parseInt(h[1], 16);
    return [(v >> 16) & 255, (v >> 8) & 255, v & 255, 1];
  }
  return null;
}
function lum([r, g, b, a], bg) {
  // composite over the background when the fg is translucent
  if (a < 1 && bg) { r = r * a + bg[0] * (1 - a); g = g * a + bg[1] * (1 - a); b = b * a + bg[2] * (1 - a); }
  const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}
function contrast(fg, bg) {
  const f = rgba(fg), b = rgba(bg);
  if (!f || !b) return null;
  const l1 = lum(f, b), l2 = lum(b);
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05);
}

const light = tokens(block(':root'));
const dark = tokens(block('html[data-theme="dark"]'));

// [foreground token, background token, minimum ratio, why]
const PAIRS = [
  ['--ink', '--bg', 4.5, 'body text'],
  ['--ink', '--surface', 4.5, 'text on cards/composer'],
  ['--ink', '--sidebar', 4.5, 'text in the rail'],
  ['--ink', '--bubble-bg', 4.5, 'user bubble text'],
  ['--ink', '--surface-2', 4.5, 'text on hover rows'],
  ['--mute', '--bg', 4.0, 'secondary text'],
  ['--mute', '--surface', 4.0, 'secondary text on cards'],
  ['--mute', '--sidebar', 4.0, 'secondary rail text'],
  ['--faint', '--bg', 3.0, 'metadata whisper'],
  ['--faint', '--surface', 3.0, 'metadata on cards'],
  ['--faint', '--surface-2', 3.0, 'labels on hover rows'],
  ['--pre-ink', '--pre-bg', 4.5, 'code text'],
  ['--on-accent', '--accent', 4.5, 'buttons / send'],
  ['--accent', '--bg', 3.0, 'links'],
  ['--accent', '--surface', 3.0, 'links on cards'],
  ['--accent', '--sidebar', 3.0, 'links in the rail'],
  ['--ok', '--bg', 3.0, 'success'],
  ['--ok', '--surface', 3.0, 'success on cards'],
  ['--bad', '--bg', 3.0, 'errors'],
  ['--bad', '--surface', 3.0, 'errors on cards'],
  ['--warn', '--bg', 3.0, 'warnings'],
  ['--warn', '--surface', 3.0, 'warnings on cards'],
  ['--violet', '--bg', 3.0, 'hljs titles'],
  ['--mute', '--bubble-bg', 3.5, 'user bubble secondary'],
];

let fail = 0;
for (const theme of ['light', 'dark']) {
  const t = theme === 'light' ? light : dark;
  console.log(`-- ${theme} --`);
  for (const [fg, bg, min, why] of PAIRS) {
    const fv = t[fg], bv = t[bg];
    if (!fv || !bv) { console.log(`MISSING ${fg}/${bg} in ${theme}`); fail++; continue; }
    const c = contrast(fv, bv);
    if (c == null) { console.log(`UNPARSEABLE ${fg}=${fv} on ${bg}=${bv}`); fail++; continue; }
    const ok = c >= min;
    if (!ok) fail++;
    console.log(`${ok ? 'ok  ' : 'FAIL'} ${fg} on ${bg}: ${c.toFixed(2)} (>= ${min}) — ${why}`);
  }
}
console.log(fail ? `${fail} contrast failures` : 'contrast: all pairs pass');
process.exit(fail ? 1 : 0);
