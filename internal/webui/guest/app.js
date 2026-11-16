// Guest view: a read-only live transcript and a box that leaves a message
// for the owner. Everything is drawn with textContent; nothing from the
// stream is parsed as HTML.
(function () {
  'use strict';
  var log = document.getElementById('log');
  var stateEl = document.getElementById('state');
  var note = document.getElementById('note');
  var form = document.getElementById('form');
  var text = document.getElementById('text');
  var send = document.getElementById('send');
  var rows = new Map();
  var stuck = true;
  var over = false;

  function el(tag, cls, txt) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (txt !== undefined) e.textContent = txt;
    return e;
  }

  log.addEventListener('scroll', function () {
    stuck = log.scrollTop + log.clientHeight >= log.scrollHeight - 40;
  });

  function build(e) {
    var row;
    if (e.kind === 'user') row = el('div', 'row user');
    else if (e.kind === 'assistant') row = el('div', 'row assistant');
    else if (e.kind === 'error') row = el('div', 'row error');
    else {
      row = el('details', 'row ' + e.kind);
      row.appendChild(el('summary'));
      row.appendChild(el('pre'));
    }
    return row;
  }

  function fill(row, e) {
    if (e.kind === 'user' || e.kind === 'assistant' || e.kind === 'error') {
      row.textContent = e.text || '';
      return;
    }
    var sum = row.firstChild;
    var pre = row.lastChild;
    if (e.kind === 'thinking') {
      sum.textContent = e.done ? 'thinking' : 'thinking...';
      pre.textContent = e.text || '';
      return;
    }
    // tool
    var label = (e.name || 'tool') + (e.summary ? ' · ' + e.summary : '') + (e.dur ? ' · ' + e.dur : '');
    sum.textContent = label;
    row.classList.toggle('running', !!e.running);
    row.classList.toggle('failed', !!e.error);
    pre.textContent = e.output || '';
  }

  function apply(e) {
    if (e.gone) {
      var old = rows.get(e.id);
      if (old) { old.remove(); rows.delete(e.id); }
      return;
    }
    var row = rows.get(e.id);
    if (!row) {
      row = build(e);
      rows.set(e.id, row);
      log.appendChild(row);
    }
    fill(row, e);
    if (stuck) log.scrollTop = log.scrollHeight;
  }

  function finish() {
    over = true;
    es.close();
    stateEl.textContent = 'ended';
    stateEl.className = 'pill';
    document.getElementById('ended').hidden = false;
    send.disabled = true;
    text.disabled = true;
  }

  var es = new EventSource('events');
  es.onmessage = function (m) {
    var ev;
    try { ev = JSON.parse(m.data); } catch (_) { return; }
    if (ev.type === 'entry' && ev.entry) apply(ev.entry);
    else if (ev.type === 'status' && ev.status) {
      stateEl.textContent = ev.status.running ? (ev.status.phase || 'working') : 'idle';
      stateEl.className = 'pill' + (ev.status.running ? ' live' : '');
    } else if (ev.type === 'hello' && ev.hello) {
      document.getElementById('title').textContent = ev.hello.title || '';
      document.getElementById('model').textContent = ev.hello.model || '';
    } else if (ev.type === 'end') finish();
  };
  es.onerror = function () {
    if (over) return;
    stateEl.textContent = 'reconnecting';
    stateEl.className = 'pill';
    // EventSource retries on its own, and gets a fresh snapshot. If the
    // link was stopped, the retry is a 404: ask once to find out.
    fetch('events', { method: 'HEAD' }).then(function (r) { if (r.status === 404) finish(); }).catch(function () {});
  };

  form.addEventListener('submit', function (ev) {
    ev.preventDefault();
    var t = text.value.trim();
    if (!t) return;
    send.disabled = true;
    fetch('say', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text: t }) })
      .then(function (r) {
        if (r.ok) { text.value = ''; note.textContent = 'Sent to the owner. It runs when they choose Send.'; return; }
        if (r.status === 404) { finish(); return; }
        return r.text().then(function (b) { note.textContent = b.trim() || 'Not sent.'; });
      })
      .catch(function () { note.textContent = 'Not sent: no connection.'; })
      .then(function () { if (!over) send.disabled = false; });
  });
  text.addEventListener('keydown', function (ev) {
    if (ev.key === 'Enter' && (ev.metaKey || ev.ctrlKey)) form.requestSubmit();
  });
})();
