// Approval card, model questions and the secret card: the three prompts
// that stop a turn and wait for the person. S.pending (the permission
// request on screen, or null) is shared: the composer's y/a/r/n/c shortcuts
// and the queue counter in the transcript read it. The ?approve=1 link
// (a notification's deep link) is consumed here. app.js passes the helpers
// it shares and uses the returned showAsk/showQuestions/showSecret (event
// stream and boot), paintAskQueue and chosenRule.
(function (global) {
  "use strict";
  global.JanetApprovals = function ({ S, esc, post, diffHTML, toolKind, markUnseen, announce, notifyAsk, input, statusEl }) {
    const ask = document.getElementById("ask");
    const askLine = document.getElementById("ask-line");
    const askRule = document.getElementById("ask-rule");
    const askRungs = document.getElementById("ask-rungs");
    const askRung = document.getElementById("ask-rung");
    const askImpact = document.getElementById("ask-impact");
    const askPreview = document.getElementById("ask-preview");
    const questionsEl = document.getElementById("questions");
    const qPrompt = document.getElementById("q-prompt");
    const qDetail = document.getElementById("q-detail");
    const qWait = document.getElementById("q-wait");
    const qOpts = document.getElementById("q-opts");
    const qOther = document.getElementById("q-other");
    const qOtherText = document.getElementById("q-other-text");
    let qWaitTimer = null;
    let pendingQs = [];
    let qIdx = 0;
    let qAnswers = [];

    // askQuestion words the card as a question about the action rather
    // than the tool's internal name: "Edit mathx.go?", "Run go test ./...?"
    // instead of "Allow edit_file — mathx.go?". The target is set in code
    // so a command's own punctuation does not run into the question mark.
    const askVerbs = { edit_file: "Edit", write_file: "Write", bash: "Run", fetch_url: "Fetch", web_search: "Search the web for" };
    function askQuestion(p) {
      askLine.textContent = "";
      const verb = askVerbs[p.name];
      askLine.append(verb && p.summary ? verb + " " : "Allow " + p.name + (p.summary ? " — " : ""));
      if (p.summary) {
        const code = document.createElement("code");
        code.textContent = p.summary;
        askLine.append(code);
      }
      askLine.append("?");
      // Warnings carries each cause on its own (a path outside the workspace,
      // a hook that asked); warning is the same line joined, for old servers.
      const warns = p.warnings && p.warnings.length ? p.warnings : p.warning ? [p.warning] : [];
      if (warns.length) {
        // Each cause on its own line, under the question, not run into it.
        for (const t of warns) {
          const w = document.createElement("span");
          w.className = "ask-warn";
          w.textContent = t;
          askLine.append(w);
        }
      }
      if (p.explain) {
        // One dim line: which layer asked and how to stop it asking.
        const why = document.createElement("span");
        why.className = "ask-source";
        why.textContent = " " + p.explain;
        askLine.append(why);
      } else if (p.source) {
        const src = document.createElement("span");
        src.className = "ask-source";
        src.dataset.source = p.source;
        src.textContent = " asked by " + ({ hook: "a hook", rule: "an ask rule", egress: "the egress policy", taint: "the taint check", protected: "a protected path", tool: "the tool's guard" }[p.source] || p.source);
        askLine.append(src);
      }
    }

    // The card says where the request comes from and how many more calls are
    // lined up behind it: tool entries that have not started yet.
    const askSrc = document.getElementById("ask-src");
    const askQueue = document.getElementById("ask-queue");
    function paintAskQueue() {
      if (!S.pending) { askQueue.hidden = true; return; }
      let more = 0;
      S.latest.forEach((e) => { if (e.kind === "tool" && !e.done && !e.running && !e.denied && !e.error) more++; });
      more = Math.max(0, more - 1);
      askQueue.hidden = more === 0;
      askQueue.textContent = more + " more waiting";
    }
    // A notification's link (?approve=1) opens on the prompt that is
    // waiting: the card scrolls into view and takes focus even when a draft
    // is half-typed, since the phone was picked up to answer it. The prompt
    // may come up a moment after the page does (the hook fires just before
    // the card), so the request stays armed for a while. The parameter is
    // taken out of the address bar so a reload does not repeat it.
    let wantApprove = false;
    function focusApproval() {
      if (!wantApprove || !S.pending) return false;
      wantApprove = false;
      ask.scrollIntoView({ block: "center" });
      ask.focus();
      return true;
    }
    try {
      const qs = new URLSearchParams(location.search);
      if (qs.get("approve")) {
        wantApprove = true;
        qs.delete("approve");
        const rest = qs.toString();
        history.replaceState(null, "", location.pathname + (rest ? "?" + rest : "") + location.hash);
        setTimeout(() => {
          if (!wantApprove) return;
          wantApprove = false;
          statusEl.textContent = "Nothing is waiting for approval now: it may already have been answered.";
        }, 20000);
      }
    } catch (_) {}

    function showAsk(p) {
      const was = ask.hidden ? null : S.pending;
      S.pending = p && p.name ? p : null;
      ask.hidden = !S.pending;
      if (!S.pending) return;
      markUnseen();
      askQuestion(p);
      askSrc.textContent = toolKind(p.name || "").k + " · " + (p.name || "tool");
      paintAskQueue();
      // A new request, not the same one sent again on a reconnect.
      const same = was && was.name === p.name && was.summary === p.summary && was.preview === p.preview;
      if (!same) {
        announce("Approval needed. " + askLine.textContent + (p.impact ? " " + p.impact : ""));
        notifyAsk(askLine.textContent);
        // Focus goes to the card, not to Allow: a stray Enter must not
        // approve a command. It stays put while the user is typing.
        const typing = document.activeElement === input && input.value.trim() !== "";
        if (!focusApproval() && !typing) ask.focus();
      } else {
        focusApproval();
      }
      showRungs(p);
      // The rung picker already names the rule; a "rule:" line above it
      // said the same thing twice.
      askRule.hidden = !p.rule || !askRungs.hidden;
      if (!askRule.hidden) askRule.textContent = "rule: " + p.rule + (p.learned ? "  ·  " + p.learned : "");
      askImpact.hidden = !p.impact;
      if (p.impact) askImpact.textContent = p.impact;
      askPreview.hidden = !p.preview;
      if (p.preview) askPreview.innerHTML = diffHTML(p.preview);
    }

    // The ladder of rules "Remember this rule" may save, narrowest first
    // (see perm.SuggestLadder). One rung is no choice at all, so the card
    // stays as it was and the line above already names the rule.
    function showRungs(p) {
      const rungs = Array.isArray(p.rungs) ? p.rungs : [];
      askRungs.hidden = rungs.length < 2;
      askRung.innerHTML = "";
      if (askRungs.hidden) return;
      rungs.forEach((r, i) => {
        const o = document.createElement("option");
        o.value = r.rule;
        o.textContent = r.rule + (r.learned ? "  ·  " + r.learned : "");
        o.selected = i === (p.ruleIndex || 0);
        askRung.appendChild(o);
      });
    }

    // chosenRule is the rung the card has selected, "" when it offers no
    // choice — the server then saves the one it proposed.
    function chosenRule() {
      return askRungs.hidden ? "" : askRung.value;
    }

    function showQuestions(qs) {
      pendingQs = Array.isArray(qs) && qs.length ? qs : [];
      qIdx = 0;
      qAnswers = [];
      questionsEl.hidden = pendingQs.length === 0;
      if (!pendingQs.length) {
        qOpts.innerHTML = "";
        qOther.hidden = true;
        showWait(null);
        return;
      }
      renderQuestion();
    }

    function renderQuestion() {
      const q = pendingQs[qIdx];
      if (!q) return;
      qPrompt.textContent = q.prompt || "Choose";
      qDetail.hidden = !q.detail;
      qDetail.textContent = q.detail || "";
      const opts = (q.options || []).concat([{ label: "Other", description: "type your own answer" }]);
      // The model's recommendation is marked and focused, so Enter takes
      // it: it is what the turn goes on with if nobody answers in time.
      qOpts.innerHTML = opts.map((o) => {
        const rec = q.recommended && o.label === q.recommended;
        return `<button type="button" data-label="${esc(o.label)}"${rec ? ' class="recommended"' : ""}>${esc(o.label)}${rec ? " (recommended)" : ""}</button>`;
      }).join("") + (q.proceeded ? `<button type="button" data-dismiss="1" class="secondary">Dismiss</button>` : "");
      qOther.hidden = true;
      qOtherText.value = "";
      showWait(q);
      const rec = qOpts.querySelector("button.recommended");
      if (rec && !(document.activeElement === input && input.value.trim() !== "")) rec.focus();
    }

    // showWait says how long the question still holds the turn ("no answer
    // in 1m12s and it proceeds with 9000"), or that the turn went on and an
    // answer now reaches the model as a late one. Nothing for a question
    // that waits as long as it takes.
    function showWait(q) {
      if (qWaitTimer) { clearInterval(qWaitTimer); qWaitTimer = null; }
      const line = () => {
        if (!q || !q.recommended) return "";
        if (q.proceeded) return "Went on with " + q.recommended + " (recommended). Answer now and the model gets it as a late answer.";
        if (!q.deadline) return "";
        const left = Math.max(0, Math.round((Date.parse(q.deadline) - Date.now()) / 1000));
        const t = left >= 60 ? Math.floor(left / 60) + "m" + String(left % 60).padStart(2, "0") + "s" : left + "s";
        return "No answer in " + t + " and it proceeds with " + q.recommended + " (recommended).";
      };
      const text = line();
      qWait.hidden = !text;
      qWait.textContent = text;
      if (text && q && !q.proceeded && q.deadline) {
        qWaitTimer = setInterval(() => { qWait.textContent = line(); }, 1000);
      }
    }

    qOpts.addEventListener("click", async (ev) => {
      if (ev.target.closest("button[data-dismiss]")) {
        await post("/api/question", { answers: pendingQs.map(() => ({ label: "skipped" })) });
        showQuestions([]);
        return;
      }
      const b = ev.target.closest("button[data-label]");
      if (!b) return;
      if (b.dataset.label === "Other") {
        qOther.hidden = false;
        qOtherText.focus();
        return;
      }
      await pickAnswer(b.dataset.label, "");
    });
    qOther.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      await pickAnswer("Other", qOtherText.value.trim());
    });

    async function pickAnswer(label, reason) {
      qAnswers.push({ label, reason });
      qIdx++;
      if (qIdx < pendingQs.length) {
        renderQuestion();
        return;
      }
      await post("/api/question", { answers: qAnswers });
      showQuestions([]);
    }

    const secretCard = document.getElementById("secret-card");
    const secretInput = document.getElementById("secret-input");
    // showSecret draws the private card for a request_secret call. The page
    // is only ever told the model's words; the value lives in this password
    // field until it is posted and is blanked straight after.
    function showSecret(s) {
      secretInput.value = "";
      secretCard.hidden = !s;
      if (!s) return;
      document.getElementById("secret-title").textContent = "Secret requested: " + (s.name || "");
      document.getElementById("secret-why").textContent = "Why: " + (s.purpose || "");
      document.getElementById("secret-use").textContent = "Use: " + (s.usage || "");
      document.getElementById("secret-env").textContent = "Available to commands as $" + (s.env_var || "") + " for this session only.";
      secretInput.focus();
    }
    async function answerSecret(declined) {
      const value = declined ? "" : secretInput.value;
      secretInput.value = "";
      if (!declined && !value) return;
      if (await post("/api/secret", { value, declined })) showSecret(null);
    }
    secretCard.addEventListener("submit", (ev) => { ev.preventDefault(); answerSecret(false); });
    document.getElementById("secret-decline").addEventListener("click", () => answerSecret(true));

    ask.addEventListener("click", async (ev) => {
      const b = ev.target.closest("button[data-d]");
      if (!b) return;
      await post("/api/permission", { decision: b.dataset.d, rule: chosenRule(), id: S.pending && S.pending.id });
    });
    return { showAsk, showQuestions, showSecret, paintAskQueue, chosenRule };
  };
})(window);
