// Numbers as the terminal prints them: textfmt.Duration and textfmt.Tokens
// ported line for line. The page printed 21.4s as "21s" and 1,000 tokens
// as "1k" while the terminal said "21.4s" and "1.0k"; make md-test checks
// this file against internal/textfmt/testdata/format.json, the table the
// Go tests read too.
(function (global) {
  "use strict";

  // tokens is textfmt.Tokens: "812", "1.5k", "12k", "2.3M". Zero is ""
  // here, where an empty count means "show nothing".
  function tokens(n) {
    if (!n) return "";
    return tokensExact(n);
  }
  function tokensExact(n) {
    if (n >= 1_000_000) return (n / 1e6).toFixed(1) + "M";
    if (n >= 10_000) return Math.floor(n / 1000) + "k";
    if (n >= 1000) return (n / 1000).toFixed(1) + "k";
    return String(n);
  }

  // duration is textfmt.Duration from milliseconds: "<1ms", "999ms",
  // "21.4s", "1m42s", "1h2m5s". Zero or less is "".
  function duration(ms) {
    if (!ms || ms <= 0) return "";
    return durationExact(ms);
  }
  function durationExact(ms) {
    if (ms < 1) return "<1ms";
    if (ms < 1000) return Math.floor(ms) + "ms";
    if (ms < 60_000) return (ms / 1000).toFixed(1) + "s";
    // time.Duration.Round(time.Second).String(): halves round up.
    let s = Math.floor((ms + 500) / 1000);
    const h = Math.floor(s / 3600);
    s -= h * 3600;
    const m = Math.floor(s / 60);
    s -= m * 60;
    return (h ? h + "h" + m + "m" : m + "m") + s + "s";
  }

  const api = { tokens, duration, tokensExact, durationExact };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  global.JanetFmt = api;
})(typeof window !== "undefined" ? window : globalThis);
