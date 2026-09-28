// Golden corpus for the markdown renderer (internal/webui/static/markdown.js).
// The renderer is security-relevant surface (it renders model output) and
// has regressed before (inline code swallowing newlines into "pills"), so
// these properties are pinned: no script/style/event-handler injection, no
// javascript: URLs, inline <code> never crosses a line, fences become the
// codeblock widget, tables parse, streaming prefixes stay sane.
// Run: node md_test.mjs   (or make md-test)
import { createRequire } from 'module';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import { readFileSync } from 'fs';

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const hlmod = require(join(here, '../static/vendor/highlight.min.js'));
globalThis.hljs = hlmod.default || hlmod;
const mdi = require(join(here, '../static/vendor/markdown-it.min.js'));
globalThis.markdownit = mdi.default || mdi;

const { render, renderOutput, codeBlocks } = require(join(here, '../static/markdown.js'));
const fmt = require(join(here, '../static/format.js'));

let pass = 0, fail = 0;
const bad = [];
function check(name, cond, extra = '') {
  if (cond) { pass++; }
  else { fail++; bad.push(`${name}${extra ? ' — ' + extra : ''}`); }
}

// --- numbers read as the terminal prints them (Go's table) ----------------
const table = JSON.parse(readFileSync(join(here, '../../textfmt/testdata/format.json'), 'utf8'));
for (const [ms, want] of table.duration_ms) check(`duration ${ms}ms`, fmt.durationExact(ms) === want, `got ${fmt.durationExact(ms)} want ${want}`);
for (const [n, want] of table.tokens) check(`tokens ${n}`, fmt.tokensExact(n) === want, `got ${fmt.tokensExact(n)} want ${want}`);

// --- security: nothing executable may survive -----------------------------
const hostile = [
  '<script>alert(1)</script>',
  '<img src=x onerror=alert(1)>',
  '<svg onload=alert(1)>',
  '<a href="javascript:alert(1)">x</a>',
  '[click](javascript:alert(1))',
  '[click](data:text/html;base64,PHNjcmlwdD4=)',
  '<iframe src="https://evil"></iframe>',
  '"><script>alert(1)</script>',
  '<?php echo 1; ?>',
];
for (const [i, src] of hostile.entries()) {
  const out = render(src, true);
  check(`hostile ${i} has no <script`, !out.includes('<script'));
  check(`hostile ${i} no raw <img>/<svg> tag`, !out.includes('<img') && !out.includes('<svg'));
  check(`hostile ${i} no executable href`, !/href="(javascript:|data:text\/html|vbscript:)/.test(out));
  check(`hostile ${i} has no <iframe`, !out.includes('<iframe'));
}

// --- the pill regression: inline code never crosses a line ----------------
const pillCase = '` lone\nnext line of prose\n` another\n`third';
for (const fin of [false, true]) {
  const out = render(pillCase, fin);
  for (const m of out.matchAll(/<code>([\s\S]*?)<\/code>/g)) {
    check(`inline code single-line (final=${fin})`, !m[1].includes('\n'), JSON.stringify(m[1].slice(0, 40)));
  }
}
// the recorded-session shape: fence fragment + heavy inline code
const session = [
  'The `INI.Parser` resolves `%(key)s` references.',
  '',
  '```python',
  'def f(self):\n    return self._raw_get("s", "k")',
  '```',
  '',
  'then `seen` and `ParseError` plus `a=%(b)s / b=%(a)s`',
  '` stray at a line start',
  'normal prose after',
].join('\n');
{
  const out = render(session, true);
  check('session: two fenced codeblocks', (out.match(/class="codeblock"/g) || []).length === 1);
  check('session: python highlighted', out.includes('hljs-') || out.includes('tk'), out.slice(0, 200));
  for (const m of out.matchAll(/<code>([\s\S]*?)<\/code>/g)) {
    check('session: inline code single-line', !m[1].includes('\n'), JSON.stringify(m[1].slice(0, 40)));
  }
  check('session: stray backtick stays literal', out.includes('` stray at a line start'));
}

// --- fences ----------------------------------------------------------------
{
  const out = render('```go\nfunc main() {}\n```', true);
  check('fence becomes codeblock widget', out.includes('class="codeblock"'));
  check('fence keeps language', out.includes('data-lang="go"'));
  check('fence hljs on final', out.includes('hljs-keyword'));
  const streaming = render('```go\nreturn fmt.Println("hi")', false);
  check('unclosed fence streams as codeblock', streaming.includes('class="codeblock"'));
  check('streaming fence uses generic tokenizer', streaming.includes('ts') && !streaming.includes('hljs-'));
  // A longer fence holds a shorter one: the inner ```js line neither
  // closes it nor lets the blank-line splitter cut the example apart.
  const nested = render('````markdown\nExample:\n\n```js\nlet x = 1\n```\n\nafter\n````\n\n# Title', true);
  check('nested fence stays one codeblock', (nested.match(/class="codeblock"/g) || []).length === 1, nested.slice(0, 300));
  check('nested fence body kept inside', !nested.includes('<p>let x') && !nested.includes('<p>after'));
  check('heading after nested fence', nested.includes('<h1>Title</h1>'));
  const infoLine = render('```\na\n\n```go\nb\n```', true);
  check('fence line with info string does not close', (infoLine.match(/class="codeblock"/g) || []).length === 1, infoLine.slice(0, 300));
  const mm = render('```mermaid\ngraph TD; A-->B\n```', true);
  check('mermaid fence marked for drawing', mm.includes('data-lang="mermaid"'));
}

// --- block basics ------------------------------------------------------------
check('heading', render('# Title', true).includes('<h1>Title</h1>'));
check('table', render('| a | b |\n|---|---|\n| 1 | 2 |', true).includes('<table>'));
check('list', render('- one\n- two', true).includes('<li>'));
check('bold', render('**x**', true).includes('<strong>'));
check('inline code', render('a `b` c', true).includes('<code>b</code>'));
check('link gets target blank', render('[a](https://x.com)', true).includes('target="_blank"'));
check('empty', render('', true) === '');
check('escape amp', render('a & b', true).includes('a &amp; b'));

// --- tool output highlighting -------------------------------------------------
{
  const out = renderOutput('read_file', 'internal/x/parser.go', 'package main\nfunc main() {}');
  check('tool output go -> hljs', out.includes('hljs-keyword'));
  const plain = renderOutput('bash', 'go build', 'total 12\n# clean');
  check('bash comment highlighted', plain.includes('tc') || plain.includes('hljs-comment'));
  const huge = 'x'.repeat(300_000);
  check('huge output stays plain', renderOutput('read_file', 'a.go', huge) === huge);
  const noLang = renderOutput('list_dir', '/tmp', 'a\nb');
  check('no-language output escaped', noLang.includes('a\nb'));
}

// --- streaming a long code block does not keep every prefix of it ---------
{
  const before = codeBlocks.size;
  let src = 'intro\n\n```go\n', out = '';
  for (let i = 0; i < 500; i++) { src += `x := ${i}\n`; out = render(src, false); }
  const grown = codeBlocks.size - before;
  check('streamed block keeps one copy source, not one per chunk', grown <= 2, `${grown} entries`);
  const bid = (/data-bid="([^"]+)"/.exec(out) || [])[1];
  check('streaming block copy source is the latest', codeBlocks.get(bid) === src.slice(src.indexOf('```go\n') + 6).replace(/\n$/, ''));
  const fin = render(src + '```', true);
  const fbid = (/data-bid="([^"]+)"/.exec(fin) || [])[1];
  check('finished block copy source kept', (codeBlocks.get(fbid) || '').endsWith('x := 499'));
}

// --- streaming sanity: prefixes of one document stay well-formed -------------
{
  const doc = 'intro paragraph\n\n```py\nx = 1\n```\n\n| a |\n|---|\n| 1 |\n\n- item\n\ntail `code` here';
  let prev = '';
  for (let n = 10; n < doc.length; n += 17) {
    const out = render(doc.slice(0, n), false);
    check(`prefix ${n}: no <script`, !out.includes('<script'));
    const opens = (out.match(/<p>/g) || []).length, closes = (out.match(/<\/p>/g) || []).length;
    check(`prefix ${n}: balanced <p>`, opens === closes, `${opens} vs ${closes}`);
    prev = out;
  }
  check('full doc final differs from last prefix (final highlighting)', render(doc, true) !== prev);
}

console.log(`md-test: ${pass} pass, ${fail} fail`);
if (bad.length) { console.log('FAILURES:'); for (const b of bad) console.log(' -', b); }
process.exit(fail ? 1 : 0);
